import type { Messages } from "../translate";

/** Stage 5d · Market (English). */
export const marketEn: Messages["market"] = {
  title: "Market",
  loadFailed: "Failed to load. Try again later.",
  retry: "Retry",
  emptyTitle: "The market is empty",
  emptyDesc:
    "Skills and agent templates appear here once an administrator imports the catalog.",
  noResults: 'No results for "{q}"',
  noItems: "Nothing in this category yet",
  resultCount: "{count} results",
  loadMore: "Load more",

  kind: {
    label: "Market type",
    skills: "Skills",
    templates: "Agent templates",
  },
  search: {
    label: "Search the market",
    skills: "Search skills",
    templates: "Search agent templates",
  },
  filter: {
    label: "Category",
    all: "All",
  },
  sort: {
    label: "Sort",
    default: "Rating",
    downloads: "Downloads",
    newest: "Newest",
    name: "Name",
  },
  packs: {
    title: "Featured packs",
    prev: "Previous page",
    next: "Next page",
    count: "{count} skills",
    unavailable: "Not in the market",
  },
  card: {
    openSkill: "View skill {name}",
    openTemplate: "View agent template {name}",
    skillCount: "{count} skills",
    verified: "Verified publisher",
  },
  picker: {
    title: "Add from the skill market",
  },
  space: {
    label: "Space",
    none: "No space available (you need write access and the space must be active).",
    loadFailed: "Failed to load spaces",
  },

  preview: {
    label: "File preview {path}",
    lines: "{count} lines",
    mode: "View mode",
    preview: "Preview",
    source: "Source",
    download: "Download file",
    downloadFile: "Download file",
    downloadFull: "Download full file",
    loadFailed: "Failed to load the file",
    reload: "Reload",
    copy: "Copy contents",
    copied: "Copied",
    copyFailed: "Copy failed",
    binary: "This file cannot be previewed ({type}, {size})",
    large: "Large file. Only the first {count} lines are shown.",
    imageInfo: "{width} × {height} · scale {scale}%",
    frontmatter: "Front matter",
    tableTruncated: "{total} rows, showing the first {shown}",
    tableTotal: "{total} rows",
    revealValue: "Show value",
  },

  skill: {
    addToAgent: "Add to Agent",
    agentTitle: "Add skill to Agents",
    agentSearch: "Search Agents",
    agentNone: "No Agents you can edit",
    agentNoMatch: "No matching Agents",
    install: "Add skill",
    installedOne: "Added “{skill}” to Agent “{name}”",
    installedMany: "Added “{skill}” to {count} Agents",
    installFailed: "Could not add the skill to {count} Agents",
    added: "Added to the space",
    openAdded: "View skill",
    addFailed: "Failed to add",
    openExisting: "Open it",
    addToSpace: "Add to space",
    add: "Add",
    pickSpace: "Choose a space to add to",
    loadFailed: "Failed to load the skill",
    origin: "Source",
    downloads: "Downloads",
    stars: "Rating",
    version: "Version",
    fileCount: "{count} files · {size}",
    tree: "Files",
    noFiles: "This skill has no files",
  },

  template: {
    use: "Create agent from template",
    pickSpace: "Choose the agent's space",
    loadFailed: "Failed to load the template",
    tabs: "Template content",
    overview: "Overview",
    instructions: "Agent instructions",
    noInstructions: "This template has no agent instructions",
    quickPrompts: "Quick prompts",
    skills: "Included skills ({count})",
    noSkills: "This template references no skills",
    unavailable: "Not in the market",
    applied:
      "Agent created. {added} skills added, {missing} not in the market, {skipped} skipped.",
    applyFailed:
      "Agent created, but the template skills could not be attached.",
  },

  update: {
    available: "Update available",
    action: "Update to {version}",
    done: "Updated to {version}",
    failed: "Failed to update",
  },

  errors: {
    exists: "This skill is already in the space",
    upToDate: "Already up to date",
    notFromMarket: "This skill was not added from the market",
    nameTaken:
      "A skill with the same name already exists in the space. Rename or delete it first.",
    versionConflict:
      "The skill was changed by someone else. Reopen it and try again.",
    skillLimit: "The space has reached its skill limit",
    agentNameTaken:
      "This Agent already has a skill with this name. Remove that binding in Agent settings before installing this version.",
    agentSkillLimit: "The agent has reached its skill limit",
    notFound: "This item is no longer in the market",
  },
};
