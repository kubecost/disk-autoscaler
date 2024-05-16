# Demo of disk-auto-scaler

The files in this directory create an automated demo that scales down a 3gb PVC to 1gb and recreates it.

This was mostly for a "kiosk mode" demo, but the examples can be applied to other use cases.

For automated continuous-scaling, see the [main readme](../README.md).

## Setup

Create all the files in this directory.

```sh
kubectl create -f ./demo
```

For a StorageClass that works with the AWS EBS CSI driver using the GP3 volume type, see [here](../aws/gp3-storageClass.yaml).

The results of the automated scaling behavior can be seen in when visualizing with Grafana.

![grafana-screenshot](demo-grafana-screenshot.png)

A Prometheus query to get the total capacity of the volumes in the kubecost Namespace is shown below.

```
sum(kubelet_volume_stats_capacity_bytes{namespace="kubecost"})
```
