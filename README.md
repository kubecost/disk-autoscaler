# Disk Auto-Scaler

> [!CAUTION]
> Disk Auto-Scaler is currently an experimental project and may not be suitable for production. Please ensure any volumes to be resized are also being backed up.

Disk Auto-Scaler is a service which keeps the disk utilization for backing PersistentVolumeClaims at a desired utilization computed from Kubecost PersistentVolume recommendations. It supports vertical scaling of supported volumes both up (increasing size) and down (decreasing size). By constantly monitoring and maintaining a target size, Disk Auto-Scaler works in tandem with Kubecost to help save you money.

## How It Works

Disk Auto-Scaler runs on a one-hour loop and asks Kubecost for volumes which are candidates for resizing (those found under Savings => Right-size persistent volumes). If any are found, it will discover the Deployment to which it is mounted and check for the presence of one or more annotations which are used to configure its operation. The target Deployment is validated and its replicas scaled to zero to ensure the disk is available. Disk Auto-Scaler will then perform a scaling using one of the methods below.

## Installation and Quick Start

Let's walk through a basic example of setting up Disk Auto-Scaler and enabling it for a given Deployment. Before beginning, ensure that Kubecost is already installed and running. For the best results, allow Kubecost to run for around 30 minutes so it has enough data.

Install Kubecost quickly with the below command.

```sh
helm repo add kubecost https://kubecost.github.io/cost-analyzer/
helm upgrade -i --create-namespace kubecost kubecost/cost-analyzer --namespace kubecost --set kubecostToken="ZGlzay1hdXRvc2NhbGVyQGt1YmVjb3N0LmNvbQ=xm343yadf98"
```

See [here](https://docs.kubecost.com/install-and-configure/install) for the full Kubecost installation documentation.

1. First, identify a Deployment with a [compatible](#limitations) PersistentVolume you would like to have Disk Auto-Scaler manage. This Deployment must be one which Kubecost has identified as a candidate for PersistentVolume rightsizing.
2. Annotate the Deployment with the following [annotations](#user-configurable-annotations) which will enable Disk Auto-Scaler with a scan interval of seven hours and a configured target utilization threshold of 80 percent.

    ```yaml
    request.autodiskscaling.kubecost.com/enabled: true
    request.autodiskscaling.kubecost.com/interval: 7h
    request.autodiskscaling.kubecost.com/targetUtilization: "80"
    ```

    ```sh
    kubectl annotate deploy busybox \
        request.autodiskscaling.kubecost.com/enabled=true \
        request.autodiskscaling.kubecost.com/interval=7h \
        request.autodiskscaling.kubecost.com/targetUtilization="80"
    ```

3. Install Disk Auto-Scaler. The below command assumes the `kubecost` Namespace already exists.

    ```sh
    kubectl create -f https://raw.githubusercontent.com/kubecost/disk-autoscaler/main/manifests/install.yaml
    ```

> [!NOTE]
> By default, this manifest assumes you have a Kubecost installed in the `kubecost` Namespace. If installed elsewhere, modify the value of the `DAS_COST_MODEL_PATH` [environment variable](#environment-variables) and then deploy.

4. Disk Auto-Scaler will automatically scan the cluster for the annotations above and scale the disks accordingly. Disk Auto-Scaler will re-check every hour for changes in utilization and scale if needed.

### Audit Mode

By default, Disk Auto-Scaler runs in audit mode which allows you to assess the changes it would make rather than having it proceed with any such changes. In this mode, it will print the pertinent information about a potential resizing action in the logs along with the savings achieved as a result as shown in the log snippet below.

```log
2024-05-16T22:44:21Z INF Namespace: thomasn-nightly-dev, Deployment: thomasn-nightly-dev-cost-analyzer, PVC: thomasn-nightly-dev-cost-analyzer, PV: pvc-691a99a8-ad61-4432-b125-fd136e8de168, Target Utilization: 70%, current size is: 32Gi, recommended size is: 2Gi, and expected monthly savings is: $3.00
```

To disable the default audit mode, change the value of the `DAS_AUDIT_MODE` environment variable to `"false"` and Disk Auto-Scaler will perform the resizing operations automatically.

For an example StorageClass resource which is supported by Disk Autoscaler, please see [here](aws/gp3-storageClass.yaml).

### Scaling Up

When scaling up, Disk Auto-Scaler increases the size of a given PVC. If the backing storage class for the PVC has `AllowVolumeExpansion` set to `true`, the claim will be modified with the new value. This allows the volume to be dynamically expanded when needed. If `AllowVolumeExpansion` is not set to `true`, the Pod copy method explained in [Scaling Down](#scaling-down) will be used instead.

### Scaling Down

When scaling down, Disk Auto-Scaler decreases the size of a given PVC. To do this, it starts a temporary Pod alongside the Deployment, attaches the volume, creates a new volume with the intended new size, and copies the data from the source to destination volume. Once the copy is completed, the source volume is removed.

## Limitations

* All license types of Kubecost are supported currently as a backend data provider. Other providers may be enabled in the future.
* Only Deployments using PersistentVolumeClaims are supported.
* A 1:1 mapping of Deployment to PVC are only supported. Multiple Deployments should not mount the same PVCs.
* Only PersistentVolumeClaims whose storage class provisioner is `ebs.csi.aws.com` are supported (i.e., only AWS EBS volumes).
* Volume Binding Mode `Immediate` is not supported due to node affinity issues related to provisioning volume before the copy operation.
* Only `WaitForFirstConsumer` is supported leveraging Kubernetes deploying PersistentVolume on the same node as the original PV backing the PVC.
* Storage class with driver `ebs.csi.aws.com` only supports the `ReadWriteOnce` access mode.

## Environment Variables

The following are the environment variables which may be passed to the Disk Auto-Scaler container along with a description and an example value.

| Env Var               | Description | Example(s) |
| --------------------- | ----------- | ------- |
| `DAS_COST_MODEL_PATH` | Location of the cost-model container for getting recommendations (and related) data. | `http://kubecost-cost-analyzer.kubecost:9090/model` |
| `DAS_KUBECONFIG`      | Path to the Kubeconfig to be used by the disk auto-scaler. | `/foo/bar` |
| `DAS_LOG_LEVEL`       | Set the desired logging level of the disk auto-scaler. Defaults to `info` if not specified. | `debug` |
| `DAS_EXCLUDE_NAMESPACES`| The namespaces are excluded from disk auto-scaling. It is recommended to include the kube-system namespace and the namespace where Kubecost is installed. This supports regular expressions. | `"kubecost,kube-*,openshift-*"`|
| `DAS_AUDIT_MODE`| Read-only execution of the Disk Auto Scaler, which offers recommended Persistent Volume (PV) sizes for deployments using the Kubecost PV right-sizing API, along with a list of cost savings predicted by Kubecost.| `"true"`|

## Annotations

### User-Configurable Annotations

The following annotations can be defined on the target Deployments meeting the requirements stated under [Limitations](#limitations).

| Annotation                                     | Description | Example(s) |
| ---------------------------------------------- | ----------- | ------- |
| `request.autodiskscaling.kubecost.com/enabled` | Opt in to disk autoscaling. | `true` |
| `request.autodiskscaling.kubecost.com/excluded` | Opt out of disk autoscaling. | `true` |
| `request.autodiskscaling.kubecost.com/interval` | The interval between each disk auto-scaling evaluation. Defaults to `7h`. Durations `m` (minutes) and `d` (days) are also supported. | `7h` |
| `request.autodiskscaling.kubecost.com/targetUtilization` | The set target utilization, as a percentage, to scale the disk. Disk auto-scaler will ensure that disk utilization is never over this set value. | `"70"` |

> [!TIP]
> AWS will not allow vertical scaling of a given volume more frequently than once every six hours. Be mindful of this limitation when setting the `request.autodiskscaling.kubecost.com/interval` annotation to a value less than or equal to `6h`.

Rather than users manually assigned to Deployments, annotations can also be written to target Deployments by `POST`ing to disk auto-scaler's `/diskAutoScaler/enable` endpoint. This action causes the annotations specified [above](#user-configurable-annotations) (minus the `/excluded` annotation) to be written by disk auto-scaler to a Deployment in the specified Namespace.

| Parameter             | Description                                                                                 |
| --------------------- | ------------------------------------------------                                            |
| `namespace`           | (required) Namespace of the Deployment.                                                     |
| `deployment`          | (required) Deployment name in the target Namespace.                                         |
| `interval`            | (required) Configures the `request.autodiskscaling.kubecost.com/interval` for the Deployment.          |
| `targetUtilization`   | (required) Configures the `request.autodiskscaling.kubecost.com/targetUtilization` for the Deployment. |

Example:

```sh
curl --location --request POST 'http://localhost:9730/diskAutoScaler/enable?namespace=gemini&deployment=prod-scout&interval=7h&targetUtilization=70'
```

Exclusions may also be configured similarly by `POST`ing to the `/diskAutoScaler/exclude` endpoint, causing disk auto-scaler to assign the `request.autodiskscaling.kubecost.com/excluded: "true"` annotation.

| Parameter    | Description                                          |
| ----------   | ------------------------------------------------     |
| `namespace`  | (required) Namespace of the Deployment.              |
| `deployment` | (required) Deployment name in the target Namespace.  |

Example:

```sh
curl --location --request POST 'http://localhost:9730/diskAutoScaler/exclude?namespace=fargo&deployment=prod-redis01'
```

### Informational Annotations

Once disk auto-scaler performs a scaling operation, the following informational annotations will be written to the target Deployment.

| Annotation                                                   | Description | Defaults |
| ------------------------------------------------------------ | ----------- | ------- |
| `request.autodiskscaling.kubecost.com/volumeExtendedBy`      | Acknowledgement that a scale operation was performed. | `kubecost_disk_auto_scaler` |
| `request.autodiskscaling.kubecost.com/volumeCreatedBy`       | Acknowledgement that a volume was created.            | `kubecost_disk_auto_scaler` |
| `request.autodiskscaling.kubecost.com/lastScaled`            | The time the volume was last scaled.                  | `2002-10-02T15:00:00Z` |

