# PRISM LB CNF

Network Service Mesh inline network function source and Kubernetes manifests.

Requires Linux, Go 1.24+, Docker, and an existing Kubernetes/NSM/OVS environment.
Build locally with `./scripts/build_local_image.sh --no-import`.
Review the node selectors and image references in `k8s/` for your environment,
then deploy with `kubectl apply -k k8s`.
The build script produces a local `nse-ovs-lb:dev` image; the manifests
reference prebuilt images. Update them to the image you built and make it
available to the target nodes before deploying.

See `PROVENANCE.md` for the source snapshot.
