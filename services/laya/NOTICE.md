# Third-party attribution

Laya is maintained by its upstream contributors, not AnchorShell.

- Runtime: [NandhaKishorM/laya](https://github.com/NandhaKishorM/laya), commit `6a5819129eb220570792e417e49723d697efd76f`, Apache-2.0. The license is reproduced in `LICENSE.laya`.
- Checkpoint: [convaiinnovations/laya-multilingual](https://huggingface.co/convaiinnovations/laya-multilingual), revision recorded in `model.json`, published as Apache-2.0.
- The encoder is mmBERT; the upstream checkpoint includes its configuration.

Weights are not included in this repository. Explicit setup preserves available upstream license and NOTICE files. Distributors of worker environments must also retain the licenses/notices supplied with PyTorch, Transformers and all locked dependencies. AnchorShell's adapter and question schema are separate files; upstream runtime code is not vendored or patched here. Setup invokes the pinned runtime's own tokenizer compatibility normalization before recording artifact checksums.
