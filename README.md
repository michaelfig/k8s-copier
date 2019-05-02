# K8S-Copier

*NOTE: This project is under active development.  Not yet working, much less ready for production.*

k8s-copier watches and updates specially-annotated Kubernetes resources with values from other resources.  The main use case is to update [Federation v2](https://github.com/kubernetes-sigs/federation-v2) Federated* resources with values from the host cluster.

**IMPORTANT: To run any of the following examples, you will first need to label your master cluster (the one which is the synchronization source for the other clusters)!**  This prevents Federation and k8s-copier from flapping when trying to update the same resource.

```
# kubectl label federatedcluster mymaster cluster-master=true
```

This is assuming your master cluster, which has been added with `kubefed2 join` and is also running the Federation control plane, is named `mymaster`.

## Cert-Manager integration

If you use [cert-manager](https://github.com/jetstack/cert-manager) and you want to update a FederatedSecret with values from a Secret that is created by cert-manager, first make sure that k8s-copier has the `--target=federatedsecrets` flag set and the `cluster-master=true` label has been applied as above.

Then, you can do something like this:

```yaml
---
apiVersion: certmanager.k8s.io/v1alpha1
kind: Certificate
metadata:
  name: cloud-certificate
  namespace: cloud
spec:
  acme:
    config:
    - dns01:
        provider: dns
      domains:
      - cloud.example.com
  dnsNames:
  - cloud.example.com
  issuerRef:
    kind: ClusterIssuer
    name: letsencrypt-prod
  secretName: cloud-certificate
---
apiVersion: types.federation.k8s.io/v1alpha1
kind: FederatedSecret
metadata:
  name: cloud-certificate
  namespace: cloud
  annotations:
    k8s-copier.fig.org/replace-spec.template.data: "secrets:cloud-certificate:data"
spec:
  placement:
    clusterSelector:
      matchExpressions:
      - key: cluster-master
        operator: DoesNotExist
```

After these resources are added, cert-manager should pick up the Certificate and create the Secret named `cloud-certificate`.  Then, k8s-copier will detect the Secret, and add its
`data` values to the FederatedSecret's `spec.template.data` path.  Finally, Federation will propagate the FederatedSecret to Secrets in the other clusters.

Whenever the Secret changes (such as when a new Certificate is ordered by cert-manager), the FederatedSecret will also be updated, and Federation will propagate the Secret to the other clusters.

## Flux integration

If you use [Weave Flux](https://github.com/weaveworks/flux) you can use the following patterns for updating federated workloads with values from a Flux-managed workload.

### Deployment

If you want to update a FederatedDeployment with values from a Deployment that is managed by Flux, first make sure that k8s-copier has the `--target=federateddeployments` flag set and the `cluster-master=true` label has been applied as above.

Then you can add a Deployment to your Flux GitOps repository:

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  annotations:
    # These are not strictly necessary... you could also update images with fluxctl.
    flux.weave.works/automated: "true"
    flux.weave.works/tag.my-container: semver:~1.0
  name: cloud-deployment
  namespace: cloud
spec:
  template:
    spec:
      containers:
      - name: my-container
      # ... whatever you want.
```

After, manually apply the FederatedDeployment (it must not be managed by Flux, or k8s-copier and Flux will flap between updating it):

```yaml
apiVersion: types.federation.k8s.io/v1alpha1
kind: FederatedDeployment
metadata:
  name: cloud-deployment
  namespace: cloud
  annotations:
    flux.weave.works/ignore: "true" # Needed to prevent flapping.
    k8s-copier.fig.org/replace-spec.template.spec: "deployments:cloud-deployment:spec"
spec:
  placement:
    clusterSelector:
      matchExpressions:
      - key: cluster-master
        operator: DoesNotExist
```

After these resources are added, Flux should manage GitOps changes and update the Deployment named `cloud-deployment`.  Then, k8s-copier will detect the Deployment changes, and add its
`spec` values to the FederatedDeployment's `spec.template.spec` path.  Finally, Federation will propagate the FederatedDeployment to Deployments in the other clusters.

Whenever the Deployment changes (such as when Flux detects image changes, or GitOps changes to the Deployment), the FederatedDeployment will also be updated, and Federation will propagate the Deployment to the other clusters.

### HelmRelease


If you want to update a FederatedHelmRelease with values from a HelmRelease that is managed by Flux, first enable federation for HelmReleases:

```
$ kubefed2 enable helmrelease
```

Next, make sure that k8s-copier has the `--target=federatedhelmreleases` flag set and the `cluster-master=true` label has been applied as above.

Then you can add a HelmRelease to your Flux GitOps repository:

```yaml
apiVersion: flux.weave.works/v1beta1
kind: HelmRelease
metadata:
  annotations:
    # These are not strictly necessary... you could also update images with fluxctl.
    flux.weave.works/automated: "true"
    flux.weave.works/tag.chart-image: semver:~1.0
  name: cloud-release
  namespace: cloud
spec:
  template:
    spec:
      values:
        image:
          repository: example.com/cloud-image
          tag: 1.0.1
      # ... whatever you want.
```

After, manually apply the FederatedHelmRelease (it must not be managed by Flux, or k8s-copier and Flux will flap between updating it):

```yaml
apiVersion: types.federation.k8s.io/v1alpha1
kind: FederatedHelmRelease
metadata:
  name: cloud-release
  namespace: cloud
  annotations:
    flux.weave.works/ignore: "true" # Needed to prevent flapping.
    k8s-copier.fig.org/replace-spec.template.spec: "helmreleases:cloud-release:spec"
spec:
  placement:
    clusterSelector:
      matchExpressions:
      - key: cluster-master
        operator: DoesNotExist
```

# Acknowledgments

Thanks to James Munnelly at Jetstack for starting me in the direction of creating an orthogonal service, rather than patching Cert-Manager and Flux.

Michael FIG <michael@fig.org>, 2019-05-02
