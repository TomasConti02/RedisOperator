This is the Redis Operator code to restart a Redis Cluster in Kubernetes. 
I developed it using the Kubebuilder framework, with Longhorn as the storage provider.
## Description
We want to create a system for restarting a Redis Cluster using VolumeSnapshots. 
It is designed to be fast and maintain persistent state.
## Getting Started
it is down the RESDME.md file
### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/redisoperator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/redisoperator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/redisoperator:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/redisoperator/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

CRD.yaml [RedisOperator is a CRD]
apiVersion: cache.example.com/v1
kind: Redis
metadata:
  name: redis-cluster
  namespace: default
spec:
  size: 1

---------------------------------------------------------------------
StorageClass.yaml [longhorn provider]
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: longhorn-new
provisioner: driver.longhorn.io
parameters:
  numberOfReplicas: "1"
  staleReplicaTimeout: "30"
  fromBackup: ""
  fsType: ext4
  dataLocality: disabled
reclaimPolicy: Delete
volumeBindingMode: Immediate

---------------------------------------------------------------------
VolumeSnapshotClass.yaml [longhorn provider]
kind: VolumeSnapshotClass
apiVersion: snapshot.storage.k8s.io/v1
metadata:
  name: new-volumesnapshotclass-longhorn
driver: driver.longhorn.io
deletionPolicy: Delete
parameters:
  type: snap
  
--------------------------------------------------------------------
kubebuilder init --domain example.com --repo github.com/username/redis-operator
kubebuilder create api --group cache --version v1 --kind Redis
mkdir -p ~/RedisOperator
cd ~/RedisOperator
go mod init example.com/RedisOperator
kubebuilder init --domain=example.com --repo=example.com/RedisOperator
kubebuilder create api --group cache --version v1 --kind Redis

--------------------------------------------------------------------
testing on ~/RedisOperator
make manifests
make install
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
make run

---------------------------------------------------------------------------
