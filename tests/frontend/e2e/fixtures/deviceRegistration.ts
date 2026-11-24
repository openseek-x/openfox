import { sign, type KeyObject } from "node:crypto";

export function deviceRegistration(
  token: string,
  keyID: string,
  keys: { publicKey: KeyObject; privateKey: KeyObject },
) {
  const parts = [Buffer.from("openfox-device-registration-v1")];
  for (const value of [token, keyID]) {
    const bytes = Buffer.from(value);
    const size = Buffer.alloc(4);
    size.writeUInt32BE(bytes.length);
    parts.push(size, bytes);
  }
  return {
    token,
    key_id: keyID,
    public_key: keys.publicKey
      .export({ type: "spki", format: "der" })
      .subarray(-32)
      .toString("base64url"),
    signature: sign(null, Buffer.concat(parts), keys.privateKey).toString(
      "base64url",
    ),
  };
}
