import { Extension } from "@tiptap/core";
import { skipTrailingNodeMeta } from "@tiptap/extensions";
import { Plugin } from "@tiptap/pm/state";

/** Focus, selection and source synchronisation are display actions, not implicit edits. */
export const InstructionDisplayTransactions = Extension.create({
  name: "instructionDisplayTransactions",
  addProseMirrorPlugins() {
    return [
      new Plugin({
        filterTransaction(transaction) {
          if (!transaction.docChanged || transaction.getMeta("preventUpdate"))
            transaction.setMeta(skipTrailingNodeMeta, true);
          return true;
        },
      }),
    ];
  },
});
