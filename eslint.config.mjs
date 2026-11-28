import typescript from "@typescript-eslint/parser";
import hooks from "eslint-plugin-react-hooks";

export default [
  { ignores: ["**/dist/**", "**/node_modules/**", "**/schema.gen.ts"] },
  {
    files: ["apps/**/*.{ts,tsx}", "packages/**/*.{ts,tsx}"],
    languageOptions: { parser: typescript },
    plugins: { "react-hooks": hooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
    },
  },
];
