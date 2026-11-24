// The CI signing step gets the private key only after the build completes.
const { createPrivateKey, createPublicKey, sign } = require("node:crypto");
const fs = require("node:fs");
const file = process.argv[2];
const key = createPrivateKey(process.env.OPENFOX_UPDATE_PRIVATE_KEY);
const publicKey = createPublicKey(key).export({ format: "jwk" });
if (
  publicKey.crv !== "Ed25519" ||
  Buffer.from(publicKey.x, "base64url").toString("base64") !==
    process.env.OPENFOX_UPDATE_PUBLIC_KEY
)
  throw new Error(
    "Signing key does not match the public key embedded in OpenFox",
  );
fs.writeFileSync(
  file + ".sig",
  sign(null, fs.readFileSync(file), key).toString("base64") + "\n",
);
