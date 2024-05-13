# Security

Kubecost Disk Autoscaler uses modern software supply chain security methods including image signing, provenance attestation (SLSA level 3 compliant), build-time image scans, and software bill of material (SBOM) both of which are also attested.

Perform the following steps to verify these artifacts for yourself.

## Discover Supply Chain Security Artifacts

To easily verify using any of the following methods, set a variable for the tag of the image you wish to inspect.

```sh
export TAG=<tag>
```

Use the [Sigstore cosign](https://github.com/sigstore/cosign) tool to show all supply chain security related artifacts available for a given image tag.

```sh
cosign tree gcr.io/kubecost1/disk-autoscaler:$TAG
```

An output similar to below will be displayed.

```
📦 Supply Chain Security Related artifacts for an image: gcr.io/kubecost1/disk-autoscaler:<tag>
└── 💾 Attestations for an image tag: gcr.io/kubecost1/disk-autoscaler:sha256-6241209ed7ee65d4f2337619baedb5f181aaa9a94a6ba284eaf40fc1d9a64917.att
   ├── 🍒 sha256:87f7e9a35a901c0acddf6bc58da8385b3dac7de5a59bf6bf6ab47b538d6704be
   ├── 🍒 sha256:4852939abd9bf1ced214e7fa23e6efab08f67ea32c4d984b4f9f7f712c0d4b6a
   └── 🍒 sha256:05b66de22d6057a2500842e505aa9a01949c3d33f9b30e83065da4a7e5ea1c47
└── 🔐 Signatures for an image tag: gcr.io/kubecost1/disk-autoscaler:sha256-6241209ed7ee65d4f2337619baedb5f181aaa9a94a6ba284eaf40fc1d9a64917.sig
   └── 🍒 sha256:1fb209e1fc2483a5554ce81293a382974295f4476abc466ed5c8748cfb48f3e3
└── 📦 SBOMs for an image tag: gcr.io/kubecost1/disk-autoscaler:sha256-6241209ed7ee65d4f2337619baedb5f181aaa9a94a6ba284eaf40fc1d9a64917.sbom
   └── 🍒 sha256:935a70c773886bfc4a5bcb1f6571aebe0bac2a72a8421275c4c3542c26b827c3
```

## Verify Image Signature

Use the [Sigstore cosign](https://github.com/sigstore/cosign) tool to verify images have been signed using the [keyless method](https://docs.sigstore.dev/signing/overview/).

```sh
cosign verify gcr.io/kubecost1/disk-autoscaler:$TAG --certificate-identity-regexp="https://github.com/kubecost1/disk-autoscaler/.github/workflows/release.yaml@refs/tags/*" --certificate-oidc-issuer="https://token.actions.githubusercontent.com" | jq
```

The image signature is also available as an offline release asset for every tagged release.

## Verify Provenance

Verify image provenance from the [SLSA standard](https://slsa.dev/).

```sh
cosign verify-attestation --type slsaprovenance02 --certificate-oidc-issuer https://token.actions.githubusercontent.com   --certificate-identity-regexp '^https://github.com/slsa-framework/slsa-github-generator/.github/workflows/generator_container_slsa3.yml@refs/tags/v[0-9]+.[0-9]+.[0-9]+$' gcr.io/kubecost1/disk-autoscaler:$TAG | jq .payload -r | base64 --decode | jq
```

If you wish, you may also use the official [SLSA verifier CLI](https://github.com/slsa-framework/slsa-verifier) with the following command.

First, find the digest of the image and tag of your choosing by using [crane](https://github.com/google/go-containerregistry/blob/main/cmd/crane/README.md).

```sh
crane digest gcr.io/kubecost1/disk-autoscaler:$TAG
```

Use `slsa-verifier` along with the digest and the tag to display the attested provenance.

```sh
slsa-verifier verify-image gcr.io/kubecost1/disk-autoscaler@<digest> --source-uri github.com/kubecost/disk-autoscaler --source-tag $TAG --print-provenance | jq
```

## Verify SBOM

Use the [Sigstore cosign](https://github.com/sigstore/cosign) tool to verify a software bill of materials (SBOM), using the [CycloneDX](https://cyclonedx.org/) standard, has been attested using the [keyless method](https://docs.sigstore.dev/signing/overview/).

```sh
cosign verify-attestation --type cyclonedx gcr.io/kubecost1/disk-autoscaler:$TAG --certificate-identity-regexp="https://github.com/kubecost1/disk-autoscaler/.github/workflows/release.yaml@refs/tags/*" --certificate-oidc-issuer="https://token.actions.githubusercontent.com" | jq .payload -r | base64 --decode | jq
```

The SBOM is also available as an offline release asset for every tagged release.

## Verify Vulnerability Scan

Verify the image scan results from [Trivy](https://github.com/aquasecurity/trivy).

```sh
cosign verify-attestation --type vuln gcr.io/kubecost1/disk-autoscaler:$TAG --certificate-identity-regexp="https://github.com/kubecost1/disk-autoscaler/.github/workflows/release.yaml@refs/tags/*" --certificate-oidc-issuer="https://token.actions.githubusercontent.com" | jq .payload -r | base64 --decode | jq
```

The vulnerability scan is also available as an offline release asset for every tagged release.
