# Testing conduct on Disk Auto Scaler

## Test case 1

A deployment with a PVC attached containing a file <=1Gi.

Sample deployment can be found in 

```
kubectl apply -f ./test/onedeployment4onepvc/deployment.yaml
```

**NOTE: The data was created in the PV using the following command:**

```
dd if=/dev/urandom of=1GB.bin bs=64M count=16 iflag=fullblock
```

The deployment was submitted to the disk autoscaler to scale it down to 2 Gi, the minimum sized PV, and the testing was successful.

## Test case 2

A deployment with two PVCs attached with varied file sizes.

The sample deployment can be found in:

```
kubectl apply -f ./test/onedeployment4twopvc/deployment.yaml
```

**NOTE: The data was created in the PVCs using the following commands in each PVC:**

```
dd if=/dev/urandom of=1GB.bin bs=64M count=16 iflag=fullblock
dd if=/dev/urandom of=2GB.bin bs=64M count=32 iflag=fullblock
```


The deployment was submitted to the disk autoscaler to scale it down to 2 Gi and 3 Gi, the minimum sized PVs, and the testing was successful.

## Test case 3

A MySQL deployment with data from publicly available APIs <1Gi.

```
kubectl apply -f test/onedeployment4onepvc/deployment.yaml
```

After creating the deployment, create tables in the MySQL deployment with the following SQL commands:

```
 CREATE TABLE population_data (id VARCHAR(15),state VARCHAR(255),year INT,population INT,PRIMARY KEY (`id`, `year`));


 CREATE TABLE population_county_data (id VARCHAR(15) ,county VARCHAR(255),year INT, population INT),PRIMARY KEY (`id`, `year`));
```

Then, insert US state/County population data to mimic MySQL deployment with data using the command: `go run ./cmd/test/main.go MySQL 127.0.0.1:3306 root das_testing@123$ usa_population`

This resulted in 382M of data. The deployment was submitted to the disk autoscaler to scale it down to 1 Gi, the minimum-sized PV, using the command:

```
kubectl annotate -n disk-scaler-mysql-demo deployment "das-mysql" "${DS_ENABLE}"
```

The testing was successful as both tables after migration had the same data after migrating the PV from 20 Gi to 1Gi.

## Test case 4

Load testing with 25 Gi of data using the deployed application.

```
kubectl apply -f test/localdeployment/deployment.yaml
```

Write 25 Gi of data using a shell script:

``` #!/bin/bash

# Number of iterations
num_iterations=25

# Loop to create files
for ((i=1; i<=$num_iterations; i++))
do
    output_file="${i}GB.bin"
    dd if=/dev/urandom of=$output_file bs=64M count=16 iflag=fullblock
done
```

**NOTE:Load testing was performed on an AWS EKS cluster to transfer up to 25GiB of data between Persistent Volumes (PVs). This load was exclusively assigned to the Disk Auto-Scaler. The process took roughly 6 minutes. Applying a heavier load to the Disk Auto-Scaler might result in a longer time to shrink the PV. During the PV shrinking process, the deployment will not be available as it is scaled down to 0.**


## Test case 5

When the recommendation is less than the actual data, it handles the situation by erroring out the disk scaling operation. However, this situation should never happen as the calculation is based on the current utilization.

## Test case 6

When the PV recommendation isn't available in Kubecost yet due to the disk provisioned being brand new, the Disk Autoscaler handles the situation by erroring out and informing the user that the utilization from Kubecost is not available.

## Test case 7

When a PV has a hostpath mount, Disk Autoscaler errors out, stating that the deployment is not a valid workload for the service.

The deployment can be found in:

```
kubectl apply -f test/hostpathpvdeployment/deployment.yaml
```

## Test case 8

Multiple PVCs in one deployment.

```
kubectl apply -f test/multiplepvcinonedeployment/deployment.yaml
```

It has 3 PVCs with 5 Gi each:

* 1st PVC - 5Gi has 1 Gi Data at 20% utilization
* 2nd PVC - 5Gi has 4 Gi Data at 80% utilization
* 3rd PVC - 5Gi has No data <1% utilization
The Disk Autoscaler performed operations to scale down the 1st and 3rd PVC to 2 Gi and 1 Gi respectively, while extending the 2nd PVC to 6 Gi.