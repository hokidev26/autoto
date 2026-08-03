// The sidebar has one interactive creation boundary: schedules stay schedules,
// while every other create action opens the directory/project flow. Chat is a
// capability of a project, not a separate creation target.
export function navigationCreateTarget({ activeWorkbench = "" } = {}) {
  return activeWorkbench === "schedules" ? "schedule" : "project";
}

const navigationCreateLabelKeys = {
  schedule: "shell.newSchedule",
  project: "shell.chooseFolder",
};

export function navigationCreateLabelKey(target) {
  return navigationCreateLabelKeys[target] || navigationCreateLabelKeys.project;
}
