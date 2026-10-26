# Trusted sandbox runtime setup

Dockerfiles derive from digest-pinned GCC/Python bases and create only a private /work directory owned by UID/GID 10001. Source, inputs and programs are never part of these image builds. The controller runs by immutable local image identity with --pull never. Setup/prefetch may use the network; untrusted execution cannot.

seccomp.json is the unchanged Moby default allowlist from commit `6fe7deb1b9fb7c0397a4593480d7d22b9ee8caef`, fetched from:

https://github.com/moby/profiles/blob/6fe7deb1b9fb7c0397a4593480d7d22b9ee8caef/seccomp/default.json

SHA-256: `6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6`.

The executor verifies this checksum, copies the profile to a private controller-side temporary file, and passes it explicitly to Docker. LICENSE.moby preserves the upstream license. Updating this profile requires reviewing/testing a new version and changing the verified hash deliberately.

The Dockerfiles' default base digests record the Session 4 trusted setup. The setup scripts resolve prefetched base tags to digests before building and record exact selected base/runtime identities in the ignored local manifest. Re-running setup is an explicit trusted dependency-update step; do not run it inside an untrusted job.
