package parser

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-openapi/spec"
)

// BuildApiDoc converts a parsed spec (v2 or v3) into a normalized, client-safe
// documentation model used by the introspection management tools.
func BuildApiDoc(specDoc interface{}, version string, toolSet *mcp.ToolSet) *mcp.ApiDoc {
	switch version {
	case VersionV3:
		if doc, ok := specDoc.(*openapi3.T); ok {
			return buildApiDocV3(doc, toolSet)
		}
	case VersionV2:
		if doc, ok := specDoc.(*spec.Swagger); ok {
			return buildApiDocV2(doc, toolSet)
		}
	}
	// Fallback: no usable raw spec, describe from the generated toolset.
	return buildApiDocFromToolSet(toolSet)
}

func buildApiDocV3(doc *openapi3.T, toolSet *mcp.ToolSet) *mcp.ApiDoc {
	out := &mcp.ApiDoc{
		Title:       doc.Info.Title,
		Version:     doc.Info.Version,
		Description: doc.Info.Description,
		Schemas:     map[string]*mcp.SchemaDoc{},
	}
	for _, s := range doc.Servers {
		out.Servers = append(out.Servers, s.URL)
	}
	for _, t := range doc.Tags {
		out.Tags = append(out.Tags, t.Name)
	}

	// DTOs / components schemas.
	if doc.Components != nil {
		seen := map[string]bool{}
		for name, ref := range doc.Components.Schemas {
			out.Schemas[name] = schemaRefToDocV3(ref, doc, seen)
		}
	}

	// Endpoints.
	for rawPath, item := range doc.Paths.Map() {
		cleanPath := rawPath
		if i := strings.Index(rawPath, "?"); i != -1 {
			cleanPath = rawPath[:i]
		}
		for method, op := range item.Operations() {
			if op == nil {
				continue
			}
			ep := mcp.EndpointDoc{
				OperationID: firstNonEmpty(op.OperationID, generateDefaultToolName(method, rawPath)),
				Summary:     op.Summary,
				Description: op.Description,
				Method:      strings.ToUpper(method),
				Path:        cleanPath,
				Tags:        op.Tags,
			}
			for _, pref := range op.Parameters {
				if pref == nil || pref.Value == nil {
					continue
				}
				p := pref.Value
				pd := mcp.ParameterDoc{
					Name:        p.Name,
					In:          p.In,
					Required:    p.Required,
					Description: p.Description,
					Schema:      schemaRefToDocV3(p.Schema, doc, nil),
				}
				ep.Parameters = append(ep.Parameters, pd)
			}
			if rb := op.RequestBody; rb != nil && rb.Value != nil {
				ep.RequestBody = mediaTypeSchemaToDocV3(rb.Value.Content, doc)
			}
			if op.Responses != nil {
				for status, rref := range op.Responses.Map() {
					if rref == nil || rref.Value == nil {
						continue
					}
					r := rref.Value
					rd := mcp.ResponseDoc{Status: status}
					if r.Description != nil {
						rd.Description = *r.Description
					}
					rd.Schema = mediaTypeSchemaToDocV3(r.Content, doc)
					ep.Responses = append(ep.Responses, rd)
				}
			}
			out.Endpoints = append(out.Endpoints, ep)
		}
	}
	collectExternalSchemasV3(doc, out, toolSet.BaseDir, toolSet.BaseURL)
	return out
}

// collectExternalSchemasV3 indexes DTOs that live in external files (referenced
// via $ref "other.yaml#/components/schemas/X") into ApiDoc.Schemas, so
// introspection can list them. Inline component schemas are already indexed;
// this pass fills in anything referenced from outside.
func collectExternalSchemasV3(doc *openapi3.T, out *mcp.ApiDoc, baseDir, baseURL string) {
	if doc == nil || doc.Paths == nil {
		return
	}
	// Cache of loaded external spec documents keyed by resolved location.
	extDocs := map[string]*openapi3.T{}
	loadExt := func(refRef string) *openapi3.T {
		path := externalFileOf(refRef)
		if path == "" {
			return nil
		}
		key := path
		loader := openapi3.NewLoader()
		loader.IsExternalRefsAllowed = true
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			// Absolute http(s) external ref: load directly by URL.
			if d, ok := extDocs[key]; ok {
				return d
			}
			u, err := url.Parse(path)
			if err != nil {
				return nil
			}
			if d, err := loader.LoadFromURI(u); err == nil {
				extDocs[key] = d
				return d
			}
			return nil
		}
		// Relative external ref: resolve against the spec's base URL (for URL
		// specs) or base directory (for local specs).
		if baseURL != "" {
			base, err := url.Parse(baseURL)
			if err == nil {
				refURL := base.ResolveReference(&url.URL{Path: path})
				key = refURL.String()
				if d, ok := extDocs[key]; ok {
					return d
				}
				if d, err := loader.LoadFromURI(refURL); err == nil {
					extDocs[key] = d
					return d
				}
				return nil
			}
		}
		if baseDir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
			key = path
		}
		if d, ok := extDocs[key]; ok {
			return d
		}
		if d, err := loader.LoadFromFile(path); err == nil {
			extDocs[key] = d
			return d
		}
		return nil
	}

	seenRef := map[string]bool{}
	var visit func(ref *openapi3.SchemaRef)
	visit = func(ref *openapi3.SchemaRef) {
		if ref == nil || ref.Ref == "" {
			return
		}
		name := refName(ref.Ref)
		if seenRef[ref.Ref] {
			return
		}
		seenRef[ref.Ref] = true

		// Inline components already indexed.
		if doc.Components != nil {
			if _, inner := doc.Components.Schemas[name]; inner {
				out.Schemas[name] = schemaRefToDocV3(ref, doc, nil)
				return
			}
		}
		// External file: load its components and index the named schema.
		if ext := loadExt(ref.Ref); ext != nil && ext.Components != nil {
			// Index all of the external file's inline components so internal
			// refs within it (e.g. LoginResponse -> Operator) resolve too.
			for n, s := range ext.Components.Schemas {
				if _, exists := out.Schemas[n]; !exists {
					out.Schemas[n] = schemaRefToDocV3(s, ext, nil)
				}
			}
		}
		// Recurse into sub-refs of the resolved value when present.
		if ref.Value == nil {
			return
		}
		for _, prop := range ref.Value.Properties {
			visit(prop)
		}
		if ref.Value.Items != nil {
			visit(ref.Value.Items)
		}
		for _, sr := range ref.Value.AllOf {
			visit(sr)
		}
		for _, sr := range ref.Value.OneOf {
			visit(sr)
		}
	}

	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op == nil {
				continue
			}
			for _, pref := range op.Parameters {
				if pref != nil && pref.Value != nil {
					visit(pref.Value.Schema)
				}
			}
			if rb := op.RequestBody; rb != nil && rb.Value != nil {
				for _, mt := range rb.Value.Content {
					if mt != nil {
						visit(mt.Schema)
					}
				}
			}
			if op.Responses != nil {
				for _, rref := range op.Responses.Map() {
					if rref != nil && rref.Value != nil {
						for _, mt := range rref.Value.Content {
							if mt != nil {
								visit(mt.Schema)
							}
						}
					}
				}
			}
		}
	}
}

// externalFileOf returns the file path portion of an external $ref (e.g.
// "schemas.yaml#/components/schemas/X" -> "schemas.yaml"), or "" for internal
// refs.
func externalFileOf(ref string) string {
	if i := strings.Index(ref, "#"); i != -1 {
		return ref[:i]
	}
	return ""
}

func mediaTypeSchemaToDocV3(content openapi3.Content, doc *openapi3.T) *mcp.SchemaDoc {
	for _, mt := range content {
		if mt != nil && mt.Schema != nil {
			return schemaRefToDocV3(mt.Schema, doc, map[string]bool{})
		}
	}
	return nil
}

// schemaRefToDocV3 converts a schema ref. seen guards against reference cycles.
func schemaRefToDocV3(ref *openapi3.SchemaRef, doc *openapi3.T, seen map[string]bool) *mcp.SchemaDoc {
	if seen == nil {
		seen = map[string]bool{}
	}
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		name := refName(ref.Ref)
		d := &mcp.SchemaDoc{Ref: ref.Ref, Name: name}
		// Resolve the reference's component to describe the DTO inline.
		target := resolveSchemaRefV3(ref.Ref, doc)
		if target != nil && !seen[ref.Ref] {
			seen[ref.Ref] = true
			resolved := schemaRefToDocV3(target, doc, seen)
			if resolved != nil {
				d.Type = resolved.Type
				d.Description = resolved.Description
				d.Format = resolved.Format
				d.Enum = resolved.Enum
				d.Properties = resolved.Properties
				d.Required = resolved.Required
				d.Items = resolved.Items
			}
			seen[ref.Ref] = false
		}
		return d
	}
	return schemaToDocV3(ref.Value, doc, seen)
}

func resolveSchemaRefV3(ref string, doc *openapi3.T) *openapi3.SchemaRef {
	if doc.Components == nil || ref == "" {
		return nil
	}
	name := refName(ref)
	return doc.Components.Schemas[name]
}

func schemaToDocV3(s *openapi3.Schema, doc *openapi3.T, seen map[string]bool) *mcp.SchemaDoc {
	if s == nil {
		return &mcp.SchemaDoc{Type: "string"} // unknown; be permissive
	}
	d := &mcp.SchemaDoc{
		Description: s.Description,
		Format:      s.Format,
		Enum:        s.Enum,
	}
	if s.Type != nil && len(*s.Type) > 0 {
		d.Type = (*s.Type)[0]
	}
	if len(s.AllOf) > 0 {
		// Merge allOf into a single object view.
		d.Type = "object"
		d.Properties = map[string]*mcp.SchemaDoc{}
		for _, sr := range s.AllOf {
			sub := schemaRefToDocV3(sr, doc, seen)
			if sub == nil {
				continue
			}
			for k, v := range sub.Properties {
				d.Properties[k] = v
			}
			d.Required = append(d.Required, sub.Required...)
		}
		return d
	}
	if s.OneOf != nil {
		// Represent unions by their alternatives.
		d.Type = "oneOf"
		d.Enum = nil
		for _, sr := range s.OneOf {
			sub := schemaRefToDocV3(sr, doc, seen)
			if sub != nil && sub.Name != "" {
				d.Description = strings.TrimSpace(d.Description + " oneOf: " + sub.Name)
			}
		}
		return d
	}
	switch d.Type {
	case "object":
		d.Properties = map[string]*mcp.SchemaDoc{}
		for pname, pref := range s.Properties {
			d.Properties[pname] = schemaRefToDocV3(pref, doc, seen)
		}
		d.Required = s.Required
	case "array":
		if s.Items != nil {
			d.Items = schemaRefToDocV3(s.Items, doc, seen)
		}
	}
	return d
}

func buildApiDocV2(doc *spec.Swagger, toolSet *mcp.ToolSet) *mcp.ApiDoc {
	out := &mcp.ApiDoc{
		Title:       doc.Info.Title,
		Version:     doc.Info.Version,
		Description: doc.Info.Description,
		Schemas:     map[string]*mcp.SchemaDoc{},
	}
	if doc.Schemes != nil {
		for _, sch := range doc.Schemes {
			out.Servers = append(out.Servers, sch+"://"+doc.Host+doc.BasePath)
		}
	} else if doc.Host != "" {
		out.Servers = append(out.Servers, doc.Host+doc.BasePath)
	}

	// Definitions / DTOs.
	for name, def := range doc.Definitions {
		out.Schemas[name] = schemaToDocV2(&def, doc.Definitions, map[string]bool{})
	}

	if doc.Paths != nil {
		for rawPath, item := range doc.Paths.Paths {
			cleanPath := rawPath
			if i := strings.Index(rawPath, "?"); i != -1 {
				cleanPath = rawPath[:i]
			}
			ops := map[string]*spec.Operation{
				"GET": item.Get, "PUT": item.Put, "POST": item.Post,
				"DELETE": item.Delete, "OPTIONS": item.Options,
				"HEAD": item.Head, "PATCH": item.Patch,
			}
			for method, op := range ops {
				if op == nil {
					continue
				}
				ep := mcp.EndpointDoc{
					OperationID: firstNonEmpty(op.ID, generateDefaultToolName(method, rawPath)),
					Summary:     op.Summary,
					Description: op.Description,
					Method:      method,
					Path:        cleanPath,
					Tags:        op.Tags,
				}
				for _, p := range op.Parameters {
					pd := mcp.ParameterDoc{
						Name:        p.Name,
						In:          p.In,
						Required:    p.Required,
						Description: p.Description,
					}
					if p.Schema != nil {
						pd.Schema = schemaToDocV2(p.Schema, doc.Definitions, map[string]bool{})
					} else {
						pd.Schema = &mcp.SchemaDoc{Type: p.Type, Format: p.Format, Enum: p.Enum}
					}
					if p.In == "body" && pd.Schema != nil {
						ep.RequestBody = pd.Schema
						continue // body param is not a header/query param
					}
					ep.Parameters = append(ep.Parameters, pd)
				}
				if op.Responses != nil {
					for statusInt, resp := range op.Responses.StatusCodeResponses {
						status := fmt.Sprintf("%d", statusInt)
						rd := mcp.ResponseDoc{Status: status}
						if resp.Schema != nil {
							rd.Description = resp.Description
							rd.Schema = schemaToDocV2(resp.Schema, doc.Definitions, map[string]bool{})
						} else {
							rd.Description = resp.Description
						}
						ep.Responses = append(ep.Responses, rd)
					}
				}
				out.Endpoints = append(out.Endpoints, ep)
			}
		}
	}
	return out
}

func schemaToDocV2(s *spec.Schema, defs spec.Definitions, seen map[string]bool) *mcp.SchemaDoc {
	if s == nil {
		return nil
	}
	if ref := s.Ref.String(); ref != "" {
		name := refName(ref)
		d := &mcp.SchemaDoc{Ref: ref, Name: name}
		if target, ok := defs[name]; ok && !seen[ref] {
			seen[ref] = true
			resolved := schemaToDocV2(&target, defs, seen)
			seen[ref] = false
			if resolved != nil {
				d.Type = resolved.Type
				d.Description = resolved.Description
				d.Format = resolved.Format
				d.Enum = resolved.Enum
				d.Properties = resolved.Properties
				d.Required = resolved.Required
				d.Items = resolved.Items
			}
		}
		return d
	}
	d := &mcp.SchemaDoc{
		Description: s.Description,
		Format:      s.Format,
		Enum:        s.Enum,
	}
	if len(s.Type) > 0 {
		d.Type = s.Type[0]
	}
	switch d.Type {
	case "object":
		d.Properties = map[string]*mcp.SchemaDoc{}
		for pname, prop := range s.Properties {
			d.Properties[pname] = schemaToDocV2(&prop, defs, seen)
		}
		d.Required = s.Required
	case "array":
		if s.Items != nil && s.Items.Schema != nil {
			d.Items = schemaToDocV2(s.Items.Schema, defs, seen)
		} else if s.Items != nil && len(s.Items.Schemas) > 0 {
			first := s.Items.Schemas[0]
			d.Items = schemaToDocV2(&first, defs, seen)
		}
	}
	return d
}

// buildApiDocFromToolSet describes an API from the generated tools alone (used
// when only a parsed ToolSet is available, e.g. tests or unparseable specs).
func buildApiDocFromToolSet(toolSet *mcp.ToolSet) *mcp.ApiDoc {
	out := &mcp.ApiDoc{
		Title:   toolSet.Name,
		Schemas: map[string]*mcp.SchemaDoc{},
	}
	for name, op := range toolSet.Operations {
		out.Endpoints = append(out.Endpoints, mcp.EndpointDoc{
			OperationID: name,
			Method:      op.Method,
			Path:        op.Path,
		})
	}
	return out
}

// refName extracts the name from a #/components/schemas/X or #/definitions/X ref.
func refName(ref string) string {
	if i := strings.LastIndex(ref, "/"); i != -1 {
		return ref[i+1:]
	}
	return ref
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
