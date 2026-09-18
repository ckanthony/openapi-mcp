package server

import "github.com/ckanthony/openapi-mcp/pkg/config"

// Tool feature groups. Each maps a management tool (or script tool) to the
// server.features flag that gates it. Tools not listed here are always-on core
// management tools (e.g. relogin, test_api_target, reload_config,
// set_log_level, preview_api_call).
const (
	featureAPIRegistration  = "api_registration"
	featureAPIIntrospection = "api_introspection"
	featureAPIExposure      = "api_exposure"
	featureKnowledge        = "knowledge"
	featureMeta             = "meta"
	featureScripts          = "scripts"
)

// Management tool -> feature group. Keys are the tool name constants.
var managementToolFeature = map[string]string{
	ToolRegisterAPI:   featureAPIRegistration,
	ToolUnregisterAPI: featureAPIRegistration,
	ToolListAPIs:      featureAPIRegistration,
	ToolReloadAPI:     featureAPIRegistration,
	ToolCheckSpec:     featureAPIRegistration,

	ToolAddTarget:          featureAPIRegistration,
	ToolRemoveTarget:       featureAPIRegistration,
	ToolListTargets:        featureAPIRegistration,
	ToolSetActiveTarget:    featureAPIRegistration,
	ToolClearActiveTarget:  featureAPIRegistration,
	ToolGetActiveTarget:    featureAPIRegistration,
	ToolSetSessionTarget:   featureAPIRegistration,
	ToolClearSessionTarget: featureAPIRegistration,
	ToolGetSessionTarget:   featureAPIRegistration,

	ToolGetAPIInfo:      featureAPIIntrospection,
	ToolListEndpoints:   featureAPIIntrospection,
	ToolGetOperation:    featureAPIIntrospection,
	ToolListSchemas:     featureAPIIntrospection,
	ToolSearchOperation: featureAPIIntrospection,
	ToolExportConfig:    featureAPIIntrospection,

	ToolUpdateAPIExposure:        featureAPIExposure,
	ToolUpdateSessionAPIExposure: featureAPIExposure,
	ToolClearSessionAPIExposure:  featureAPIExposure,
	ToolAPIExposure:              featureAPIExposure,

	ToolKnowledgeInit:        featureKnowledge,
	ToolKnowledgeLoad:        featureKnowledge,
	ToolKnowledgeStatus:      featureKnowledge,
	ToolKnowledgeUpsert:      featureKnowledge,
	ToolKnowledgeDelete:      featureKnowledge,
	ToolKnowledgeGet:         featureKnowledge,
	ToolKnowledgeSearch:      featureKnowledge,
	ToolKnowledgeClarify:     featureKnowledge,
	ToolKnowledgeRemember:    featureKnowledge,
	ToolKnowledgeSuggestions: featureKnowledge,
	ToolKnowledgePromote:     featureKnowledge,
	ToolKnowledgeReview:      featureKnowledge,
	ToolKnowledgeSync:        featureKnowledge,
	ToolUpdateKnowledge:      featureKnowledge,
	ToolCapabilities:         featureKnowledge,
	ToolDiscoverTask:         featureKnowledge,
	ToolRunTask:              featureKnowledge,
	ToolView:                 featureKnowledge,

	ToolMetaInit:            featureMeta,
	ToolMetaStatus:          featureMeta,
	ToolMetaSync:            featureMeta,
	ToolUpdateMetaKnowledge: featureMeta,

	ToolScriptList:     featureScripts,
	ToolScriptDescribe: featureScripts,
	ToolPromoteScript:  featureScripts,
}

// toolFeatureGated reports whether toolName lives behind a feature flag.
func toolFeatureGated(toolName string) bool {
	_, ok := managementToolFeature[toolName]
	return ok
}

// featuresEnabled reports whether the feature group gating toolName is enabled
// under the given feature config. Always-on core tools return true.
func featuresEnabled(f config.FeaturesConfig, toolName string) bool {
	switch managementToolFeature[toolName] {
	case featureAPIRegistration:
		return f.APIRegistrationEnabled()
	case featureAPIIntrospection:
		return f.APIIntrospectionEnabled()
	case featureAPIExposure:
		return f.APIExposureEnabled()
	case featureKnowledge:
		return f.KnowledgeEnabled()
	case featureMeta:
		return f.MetaEnabled()
	case featureScripts:
		return f.ScriptsEnabled()
	}
	return true
}
