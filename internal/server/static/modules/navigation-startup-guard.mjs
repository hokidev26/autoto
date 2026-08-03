// A startup restore is allowed to write navigation only while both the
// initialization lifecycle and the user's navigation intent are unchanged.
export function createNavigationStartupGuard() {
  let initSeq = 0;
  let lifecycleSeq = 0;
  let navigationIntentSeq = 0;
  let startupBlockedByUser = false;

  function tokenFor(currentInitSeq = initSeq, startupAllowed = !startupBlockedByUser) {
    return {
      initSeq: currentInitSeq,
      lifecycleSeq,
      navigationIntentSeq,
      startupAllowed,
      startupBlockedByUser,
    };
  }

  return {
    beginInit(currentInitSeq) {
      initSeq = Number(currentInitSeq) || 0;
      lifecycleSeq += 1;
      // A later init must not turn an earlier user click into a fresh startup
      // permission. A new page/module instance creates a new guard, so this
      // remains scoped to the current runtime lifecycle.
      return tokenFor(initSeq);
    },

    beginUserNavigation() {
      navigationIntentSeq += 1;
      startupBlockedByUser = true;
      return navigationIntentSeq;
    },

    invalidate() {
      // Invalidate old startup work without pretending that an auth/lifecycle
      // change was a user navigation. A clean auth restart can still restore
      // when no user navigation happened in this runtime instance.
      lifecycleSeq += 1;
      return lifecycleSeq;
    },

    capture(currentInitSeq = initSeq) {
      return tokenFor(currentInitSeq);
    },

    isCurrent(token) {
      return Boolean(token)
        && token.startupAllowed !== false
        && !startupBlockedByUser
        && token.initSeq === initSeq
        && token.lifecycleSeq === lifecycleSeq
        && token.navigationIntentSeq === navigationIntentSeq;
    },

    snapshot() {
      return tokenFor();
    },
  };
}
