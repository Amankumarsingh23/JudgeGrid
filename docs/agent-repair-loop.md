# Bounded agent repair

Session 20 is an operator-controlled child of a real Session 19 validation and a Session 18 selected workspace. It accepts only one authoritative failing root-package Go validation, with its report and log digests verified by the existing reader. TEST_FAILURE and BUILD_FAILURE can support diagnosis. PASS, infrastructure, policy, timeout, memory and output-limit outcomes are ineligible to start a repair.

Diagnosis selects a single operator-nominated editable source file from the exact current workspace. It checks the current version and the file hash before returning a bounded source span. It returns up to 8 KiB of verified source and up to 8 KiB of the sanitized log. The log remains untrusted even when its integrity is verified. No log text can grant authority or expand the read scope.

The versioned proposer accepts this typed evidence and returns one patch or insufficient evidence. The shipped deterministic fixture recognizes tightly scoped examples. The trusted operator may instead submit a strict typed patch. There is no coding-model provider, remote source transfer, or automated general-purpose bug-fix claim. The fixture can show that a proposal process works on controlled examples.

Every iteration goes through the real workspace and validation controllers. A passed revalidation means the selected root-package checks passed against that exact version and image. A failed first patch can be followed by a newly proposed and newly approved second patch. If evidence runs out or an iteration hits an unsupported failure, the controller stops without another speculative edit.
