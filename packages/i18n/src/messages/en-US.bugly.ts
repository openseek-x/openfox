import type { Messages } from "../translate";
export const buglyEn: Messages["integrations"]["bugly"] = {
  title: "Bugly Professional inbound tasks",
  capability: "Receive task webhooks",
  scope:
    "Supports manual task creation from Bugly Professional in China only. Creates tasks in bound spaces; status writeback, historical import and personal binding are unavailable.",
  authority:
    "Enabling allows anyone holding the secret URL to submit tasks for bound products. It does not verify Tencent credentials or a request signature. Keep the URL private.",
  notConfigured:
    "The server public URL or encryption key is not configured. Contact your deployment administrator.",
  create: "Create inbound connection",
  createdDisabled:
    "New connections start disabled. Enable reception explicitly.",
  enable: "Enable reception",
  disable: "Disable reception",
  reconnect: "Reconnect and enable",
  rotate: "Rotate secret URL",
  disconnect: "Disconnect",
  enableHelp:
    "Anyone holding the secret URL can create tasks for bound products. Duplicate sources return the original task without overwriting local changes.",
  disableHelp:
    "Reception is rejected while disabled. The URL, bindings and existing tasks are retained.",
  reconnectHelp:
    "Reconnecting creates a new secret URL. Existing tasks and mappings remain; bindings needing attention are not automatically resumed.",
  rotateHelp:
    "The old URL stops working immediately. Update it in Bugly manually. The enabled or disabled state is retained.",
  disconnectHelp:
    "The URL stops working immediately. Tasks, bindings and source mappings remain. Reconnecting creates a new URL.",
  showSecret: "Show secret URL",
  hideSecret: "Hide secret URL",
  secret: "Secret URL",
  copySecret: "Copy URL",
  copied: "Copied",
  copyFailed: "Could not copy. Copy the displayed URL manually.",
  bindings: "Product bindings",
  addBinding: "Bind product",
  editBinding: "Edit product binding",
  noBindings: "No products are bound. Unbound ProductIDs cannot create tasks.",
  product: "ProductID",
  productHelp:
    "Enter the original case-sensitive Bugly Professional ProductID manually. No product discovery or credential test is performed.",
  projectName: "Project display name",
  space: "Target space",
  type: "Default task type",
  spaceHelp:
    "Only active spaces you can manage are listed. The server checks permissions again when saving and receiving.",
  chooseSpace: "Choose space",
  chooseType: "Choose an available type",
  noTypes: "This space has no enabled, unarchived task types.",
  fixedTarget:
    "The product, target space and original authorizer are fixed after creation. Remove and rebind to change spaces; the same issue may create another task.",
  triage:
    "Existing authorized field triage rules remain in effect. This inbound entry does not move tasks into unbound spaces through triage rules.",
  priority: "Priority mapping",
  priorityHelp:
    "Up to 20 exact external values. Unmapped values use None. The current organization priority scheme is retained without automatic upgrade.",
  externalPriority: "External priority",
  targetPriority: "OpenFox priority",
  addPriority: "Add priority mapping",
  removePriority: "Remove mapping {index}",
  priorityInvalid:
    "External values must be nonempty, unique and at most 64 characters. Lowest is available only with the current five-level scheme.",
  pauseHelp:
    "Stop receiving this product while retaining its tasks and source mappings.",
  pause: "Pause",
  resume: "Resume",
  remove: "Remove binding",
  removeHelp:
    "This product will stop receiving tasks. Existing tasks and mappings remain. Adding it again creates a new binding and may create another task for the same Bugly issue.",
  resumeHelp:
    "Resuming rechecks the original authorizer’s current organization and target-space permissions and task type. It does not borrow the current administrator’s identity.",
  deliveries: "Recent receipts",
  deliveriesHelp:
    "Shows the last 50 processing results without raw payloads, secret URLs or platform credentials.",
  noDeliveries: "No receipts yet.",
  refresh: "Refresh",
  loading: "Loading settings…",
  loadFailed: "Could not load Bugly settings. Refresh to retry.",
  saveFailed:
    "The operation did not complete. Refresh and check the current state before explicitly retrying.",
  conflict:
    "The configuration version changed and the list is refreshing. Close and reopen the operation, then review the latest settings before submitting.",
  forbidden:
    "You do not have the required organization or space management permissions.",
  inactive:
    "The connection is disabled or disconnected. Check its state first.",
  invalid: "Check the product, space, task type and priority mapping.",
  rollbackDisabled: "Reception paused after an application rollback.",
  unavailable:
    "The binding or source resource is unavailable. Refresh and check it.",
  states: {
    ACTIVE: "Enabled",
    NEEDS_ATTENTION: "Needs attention",
    DISCONNECTED: "Disconnected",
    PAUSED: "Paused",
    REMOVED: "Removed",
  },
  receipt: {
    PROCESSED: "Processed",
    IGNORED: "Ignored",
    REJECTED: "Rejected",
    FAILED: "Failed",
  },
  details: {
    created: "Task created",
    duplicate: "Duplicate source; original task returned",
    invalid_payload: "Invalid submission",
    product_not_bound: "Product not bound",
    binding_inactive: "Binding not active",
    authorization_lost: "Original authorizer lost access",
    task_type_unavailable: "Task type unavailable",
    source_task_unavailable: "Source task unavailable",
    task_access_lost: "Source task access lost",
    creation_failed: "Task creation failed",
  },
};
