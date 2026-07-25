package plugins

import "testing"

func TestPluginToolCatalogMetadataIsNonSecretAndGrouped(t *testing.T) {
	tool := &pluginTool{pluginID: "plugin-1", remoteName: "remote_search", exposedName: "plugin__remote_search", description: "Search"}
	metadata := tool.CatalogMetadata()
	if metadata.Domain != "plugins" || metadata.DisplayName != "remote_search" || metadata.Source != "plugin" || metadata.SourceID != "plugin-1" {
		t.Fatalf("unexpected plugin catalog metadata: %+v", metadata)
	}
}
