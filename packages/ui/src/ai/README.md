# prompt-kit adapters

Source: https://github.com/ibelick/prompt-kit

Pinned revision: `5a9496635cc0e1bf7679fe2a9e35d9b36b99c08a` (retrieved 2026-11-07).

These are locally maintained adaptations of `components/prompt-kit/prompt-input.tsx`,
`message.tsx`, `code-block.tsx`, and `chain-of-thought.tsx`. The chat-only renderer at
`packages/app/src/conversations/ChatMarkdown.tsx` adapts `markdown.tsx`.

Local differences:

- Use OpenFox tokens, icons, controls and translations, without a second shadcn theme.
- PromptInput is controlled. Only Composer handles submit keys, IME and send preferences.
  The forwarded textarea ref preserves mentions, selection and quick replies; autosize
  tracks text and width. Container clicks do not steal focus from action menus.
- Message content delegates Markdown to the application rather than coupling the UI
  package to a parser. Actions are keyboard/touch accessible without relying on hover.
- Code blocks reuse OpenFox's lazy, bounded syntax highlighter; no Shiki or raw HTML.
- ChainOfThought is a presentation-only adapter. OpenFox owns disclosure, state,
  expand-all and event folding; no Radix dependency or duplicate disclosure state.
- Markdown uses react-markdown + GFM + soft breaks, parsing the complete message so
  reference links work across blocks. Unchanged messages are memoized. Raw HTML is
  omitted, URLs use the existing protocol allowlist, and remote images render alt text
  without third-party requests. Existing attachment previews are unchanged.
- Generated footnotes use per-message IDs and local scroll/focus without changing
  the app route; other fragment/relative URLs stay blocked. Clipboard feedback is
  discarded when the copied code changes or unmounts before the request finishes.
- No ChatContainer, tool state mapping, remote favicons, or animation dependencies.

Do not blindly replace these files with registry output. Recheck controlled input,
IME, clipboard, focus, links, images, partial code fences, scroll/search behavior,
expanded steps, dark mode and narrow layouts when updating upstream.

## Upstream license

MIT License

Copyright (c) 2025 Julien Thibeaut

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
