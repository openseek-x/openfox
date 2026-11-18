const UUID =
  "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";
const iconPath = new RegExp(`^/api/v1/organizations/${UUID}/icons/${UUID}$`);
const iconID = new RegExp(`^${UUID}$`);

export function isUploadedIconURL(value: string): boolean {
  return iconPath.test(value);
}

export function uploadedIconURL(orgId: string, id: string): string | null {
  return iconID.test(orgId) && iconID.test(id)
    ? `/api/v1/organizations/${orgId}/icons/${id}`
    : null;
}
