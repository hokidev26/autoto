package db

// Domain substores hold the former god-Store method sets. Each embeds *Store so
// method bodies keep using s.db, s.reader(), and sibling domain methods without
// SQL or signature changes. Store embeds the substores so those methods are
// promoted: external callers still write store.ListSchedules(...) unchanged.
//
// Distinct types are required. A shared alias would embed the same type twice
// and make promoted names ambiguous.

type accountPreferenceStore struct{ *Store }
type agentRoleStore struct{ *Store }
type agentRuntimeSnapshotStore struct{ *Store }
type apiRequestStore struct{ *Store }
type backendStore struct{ *Store }
type backgroundTaskStore struct{ *Store }
type channelStore struct{ *Store }
type contextAskStore struct{ *Store }
type deviceActionStore struct{ *Store }
type executionStore struct{ *Store }
type gatewayStore struct{ *Store }
type generatedImageStore struct{ *Store }
type integrationStore struct{ *Store }
type lifecycleHookStore struct{ *Store }
type liveSnapshotStore struct{ *Store }
type mcpStore struct{ *Store }
type memoryStore struct{ *Store }
type messageStore struct{ *Store }
type modelAggregateStore struct{ *Store }
type notificationStore struct{ *Store }
type oauthAppStore struct{ *Store }
type planStore struct{ *Store }
type pluginStore struct{ *Store }
type projectStore struct{ *Store }
type promptStore struct{ *Store }
type providerAccountStore struct{ *Store }
type providerSecretStore struct{ *Store }
type remoteCollaborationStore struct{ *Store }
type runStore struct{ *Store }
type runtimeSettingsStore struct{ *Store }
type scheduleStore struct{ *Store }
type skillStore struct{ *Store }
type specStore struct{ *Store }
type storedDefStore struct{ *Store }
type toolAvailabilityStore struct{ *Store }
type toolCallStore struct{ *Store }
type toolExecutionGroupStore struct{ *Store }
type userStore struct{ *Store }

func wireSubstores(s *Store) {
	if s == nil {
		return
	}
	s.accountPreferenceStore = &accountPreferenceStore{Store: s}
	s.agentRoleStore = &agentRoleStore{Store: s}
	s.agentRuntimeSnapshotStore = &agentRuntimeSnapshotStore{Store: s}
	s.apiRequestStore = &apiRequestStore{Store: s}
	s.backendStore = &backendStore{Store: s}
	s.backgroundTaskStore = &backgroundTaskStore{Store: s}
	s.channelStore = &channelStore{Store: s}
	s.contextAskStore = &contextAskStore{Store: s}
	s.deviceActionStore = &deviceActionStore{Store: s}
	s.executionStore = &executionStore{Store: s}
	s.gatewayStore = &gatewayStore{Store: s}
	s.generatedImageStore = &generatedImageStore{Store: s}
	s.integrationStore = &integrationStore{Store: s}
	s.lifecycleHookStore = &lifecycleHookStore{Store: s}
	s.liveSnapshotStore = &liveSnapshotStore{Store: s}
	s.mcpStore = &mcpStore{Store: s}
	s.memoryStore = &memoryStore{Store: s}
	s.messageStore = &messageStore{Store: s}
	s.modelAggregateStore = &modelAggregateStore{Store: s}
	s.notificationStore = &notificationStore{Store: s}
	s.oauthAppStore = &oauthAppStore{Store: s}
	s.planStore = &planStore{Store: s}
	s.pluginStore = &pluginStore{Store: s}
	s.projectStore = &projectStore{Store: s}
	s.promptStore = &promptStore{Store: s}
	s.providerAccountStore = &providerAccountStore{Store: s}
	s.providerSecretStore = &providerSecretStore{Store: s}
	s.remoteCollaborationStore = &remoteCollaborationStore{Store: s}
	s.runStore = &runStore{Store: s}
	s.runtimeSettingsStore = &runtimeSettingsStore{Store: s}
	s.scheduleStore = &scheduleStore{Store: s}
	s.skillStore = &skillStore{Store: s}
	s.specStore = &specStore{Store: s}
	s.storedDefStore = &storedDefStore{Store: s}
	s.toolAvailabilityStore = &toolAvailabilityStore{Store: s}
	s.toolCallStore = &toolCallStore{Store: s}
	s.toolExecutionGroupStore = &toolExecutionGroupStore{Store: s}
	s.userStore = &userStore{Store: s}
}
