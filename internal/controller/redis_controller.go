/*
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
*/
package controller

import (
	"context"
	"fmt"
	"time"

	cachev1 "example.com/RedisOperator/api/v1"
	volumesnapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var statefulSet *appsv1.StatefulSet
var restarted *appsv1.StatefulSet
var snapshot bool = false
var size int
var pvc bool = false
var State = "Creation"

// RedisReconciler reconciles a Redis object
type RedisReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cache.example.com,resources=redis,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cache.example.com,resources=redis/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cache.example.com,resources=redis/finalizers,verbs=update
// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Redis object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
// For more details, check Reconcile and its Result here:
/*
	This is my personal Kubernetes Redis Operator developed using the Kubebuilder framework.
	I use Longhorn as a storage provider and deploy it on my cluster.
	I have created and deployed a personal StorageClass and VolumeSnapshotClass.
	Scope -> Create a stateful Redis Cluster using the StatefulSet Kubernetes object and
	implement a restart strategy for it. I use Kubernetes VolumeSnapshot for backup.
	I want to restart the entire cluster while minimizing downtime and maintaining the Redis dataset state.
	I will be working in the default namespace of Kubernetes (my personal choice).
	The Redis persistent system creates a binary snapshot of the dataset (.rdb) and stores it on a PV.
*/
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *RedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //This functions will be execute every system status change.
	//STATEFULSET REDIS
	logger := log.FromContext(ctx)
	// Fetch the Redis instance (the CRD)
	redis := &cachev1.Redis{} // I deploy the CRD manifests for my personal controller on the cluster
	err := r.Get(ctx, req.NamespacedName, redis)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Define ConfigMap for Redis
	// The Redis container will mount it to configure the application service.
	configMap := r.configMapForRedis(redis)
	if err := controllerutil.SetControllerReference(redis, configMap, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	foundConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configMap.Name, Namespace: configMap.Namespace}, foundConfigMap)
	if err != nil && client.IgnoreNotFound(err) == nil {
		err = r.Create(ctx, configMap) //deploy a configMap on the cluster
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	// Define different states for my personal restart strategy
	if State == "Creation" && r.areAllPodsReady(ctx, redis.Spec.Size) {
		State = "BackUp" // System is ready for backup using VolumeSnapshot
		logger.Info("BackUp started")
	}
	if State == "BackUp" && r.areAllVolumeSnapshotsReady(ctx, redis.Spec.Size) {
		State = "PVCRestart"
		logger.Info("PVCRestart started")
	}
	if State == "PVCRestart" && r.areAllPVCsBound(ctx, redis.Spec.Size) {
		State = "Restart"
		logger.Info("Restarted system started")
	}
	// Different operations that the controller will perform based on the current state.
	switch State {
	case "Creation":
		// Each Redis Pod in the cluster will mount a PVC for storage volume on a cluster node.
		statefulSet = r.statefulSetForRedis(redis, redis.Spec.Size)
		if err := controllerutil.SetControllerReference(redis, statefulSet, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		foundStatefulSet := &appsv1.StatefulSet{}
		err = r.Get(ctx, types.NamespacedName{Name: statefulSet.Name, Namespace: statefulSet.Namespace}, foundStatefulSet)
		if err != nil && client.IgnoreNotFound(err) == nil {
			logger.Info("StatefulSet Creation")
			err = r.Create(ctx, statefulSet) // Define and deploy a StatefulSet to configure a stateful Redis cluster
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	case "BackUp":
		if !snapshot {
			time.Sleep(50 * time.Second) // i want have time for change the Redis keys and create a dump.rdb
			// Create all VolumeSnapshots for all the Redis Pods. These will serve as our backup resources.
			if err := r.createVolumeSnapshots(redis); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create volume snapshots: %v", err)
			}
			logger.Info("VolumeSnapshot Creation, State :" + State)
			snapshot = true
		}
	case "PVCRestart":
		if !pvc {
			// Before defining a new Redis cluster, we need the new PVC for restarting the cluster.
			// We will use the VolumeSnapshot from the previous step as the data source.
			if err := r.CreatePVCs(r.Client, redis.Namespace, "redis-data-redis-test", redis.Spec.Size, "redis-snapshot", "longhorn-new", redis); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create PVCs: %v", err)
			}
			logger.Info("PVC Restart Creation")
			pvc = true
		}
	case "Restart":
		// Now we can define and create a new StatefulSet for the restarted Redis cluster.
		// !! I will mount the PVC Restart from the previous step to the new Redis container. The new system will have the backup state.
		restarted = r.statefulSetForRedisRestarted(redis, redis.Spec.Size)
		if err := controllerutil.SetControllerReference(redis, restarted, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		foundStatefulSetRestarted := &appsv1.StatefulSet{}
		err = r.Get(ctx, types.NamespacedName{Name: restarted.Name, Namespace: restarted.Namespace}, foundStatefulSetRestarted)
		if err != nil && client.IgnoreNotFound(err) == nil {
			err = r.Create(ctx, restarted)
			if err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("StatefulSet Restarted")
		}
	}
	return ctrl.Result{}, nil
}
func (r *RedisReconciler) configMapForRedis(redis *cachev1.Redis) *corev1.ConfigMap {
	labels := map[string]string{
		"app": redis.Name,
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-config",
			Namespace: redis.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"redis.conf": `
			port 6379
			save 5 1
            dir /data`,
		},
	}
}

func (r *RedisReconciler) CreatePVCs(c client.Client, namespace string, baseName string, numPVCs int32, snapshotName string, storageClassName string, redis *cachev1.Redis) error {
	storageSize, _ := resource.ParseQuantity("1Gi")
	for i := int32(0); i < numPVCs; i++ {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", baseName, i),
				Namespace: namespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				StorageClassName: &storageClassName,
				DataSource: &corev1.TypedLocalObjectReference{
					Name:     fmt.Sprintf("%s-%d", snapshotName, i),
					Kind:     "VolumeSnapshot",
					APIGroup: pointer.StringPtr("snapshot.storage.k8s.io"),
				},
			},
		}
		if err := controllerutil.SetControllerReference(redis, pvc, r.Scheme); err != nil {
			return fmt.Errorf("error setting controller reference for VolumeSnapshot %s: %v", pvc, err)
		}
		if err := c.Create(context.Background(), pvc); err != nil {
			return err
		}
	}
	return nil
}
func (r *RedisReconciler) createVolumeSnapshots(redis *cachev1.Redis) error {
	size := redis.Spec.Size
	for i := int32(0); i < size; i++ {
		pvcName := fmt.Sprintf("redis-data-redis-cluster-%d", i) // name of PVC for backup
		snapshotName := fmt.Sprintf("redis-snapshot-%d", i)

		snapshot := &volumesnapshotv1.VolumeSnapshot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      snapshotName,
				Namespace: redis.Namespace,
			},
			Spec: volumesnapshotv1.VolumeSnapshotSpec{
				VolumeSnapshotClassName: ptr("new-volumesnapshotclass-longhorn"),
				Source: volumesnapshotv1.VolumeSnapshotSource{
					PersistentVolumeClaimName: ptr(pvcName),
				},
			},
		}
		// i need define that the personal controller will be the owner
		if err := controllerutil.SetControllerReference(redis, snapshot, r.Scheme); err != nil {
			return fmt.Errorf("error setting controller reference for VolumeSnapshot %s: %v", snapshotName, err)
		}
		// Create the VolumeSnapshot
		if err := r.Client.Create(context.Background(), snapshot); err != nil {
			return fmt.Errorf("error creating VolumeSnapshot %s: %v", snapshotName, err)
		}
	}

	return nil
}
func (r *RedisReconciler) statefulSetForRedisRestarted(redis *cachev1.Redis, size int32) *appsv1.StatefulSet {
	labels := map[string]string{
		"app": redis.Name,
	}
	storage, _ := resource.ParseQuantity("1Gi")
	storageClassName := "longhorn-new"
	volumeClaimTemplates := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis-data",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storage,
					},
				},
				StorageClassName: &storageClassName,
			},
		},
	}
	for i := range volumeClaimTemplates {
		controllerutil.SetControllerReference(redis, &volumeClaimTemplates[i], r.Scheme)
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-test",
			Namespace: redis.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "redis-test-service",
			Replicas:    &size, // size of cluster
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:latest",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 6379,
									Name:          "redis",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "redis-data", // Name of volume mount. Same of Redis persistent snapshot
									MountPath: "/data",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: volumeClaimTemplates,
		},
	}
}
func (r *RedisReconciler) areAllPodsReady(ctx context.Context, size int32) bool {
	pods := &corev1.PodList{}
	listOps := &client.ListOptions{
		Namespace: "default",
	}
	if err := r.List(ctx, pods, listOps); err != nil {
		return false
	}
	if len(pods.Items) != int(size) {
		return false
	}
	// check if all Pod are Ready to use
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
	}
	return true
}
func (r *RedisReconciler) areAllVolumeSnapshotsReady(ctx context.Context, size int32) bool {
	logger := log.FromContext(ctx)
	snapshots := &volumesnapshotv1.VolumeSnapshotList{}
	listOps := &client.ListOptions{
		Namespace: "default",
	}
	if err := r.List(ctx, snapshots, listOps); err != nil {
		return false
	}
	logger.Info(fmt.Sprintf("Trovati VolumeSnapshot: %d", len(snapshots.Items)))
	if len(snapshots.Items) != int(size) {
		return false
	}
	// check if the Volumesnapshot are ready to use before the next step
	for _, snapshot := range snapshots.Items {
		if snapshot.Status == nil || snapshot.Status.ReadyToUse == nil || !*snapshot.Status.ReadyToUse {
			return false
		}
	}
	return true
}
func (r *RedisReconciler) areAllPVCsBound(ctx context.Context, size int32) bool {
	pvcs := &corev1.PersistentVolumeClaimList{}
	listOps := &client.ListOptions{
		Namespace: "default",
	}
	if err := r.List(ctx, pvcs, listOps); err != nil {
		fmt.Printf("Error listing PVCs: %v\n", err)
		return false
	}
	fmt.Printf("PVCs found (%d):\n", len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		fmt.Printf("- %s\n", pvc.Name)
	}
	if len(pvcs.Items) != int(size*2) {
		fmt.Printf("Expected %d PVCs, but found %d\n", size, len(pvcs.Items))
		return false
	}
	for _, pvc := range pvcs.Items {
		if pvc.Status.Phase != corev1.ClaimBound {
			fmt.Printf("PVC %s is not bound, status: %v\n", pvc.Name, pvc.Status.Phase)
			return false
		}
	}
	return true
}
func (r *RedisReconciler) statefulSetForRedis(redis *cachev1.Redis, DimensioneDeploy int32) *appsv1.StatefulSet {
	labels := map[string]string{
		"app": redis.Name,
	}
	storage, _ := resource.ParseQuantity("1Gi")
	storageClassName := "longhorn-new"
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redis.Name,
			Namespace: redis.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: redis.Name,
			Replicas:    &DimensioneDeploy,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: "redis:latest",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 6379,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "redis-data",
									MountPath: "/data",
								},
								{
									Name:      "redis-config",
									MountPath: "/usr/local/etc/redis/redis.conf",
									SubPath:   "redis.conf",
								},
							},
							Command: []string{"redis-server", "/usr/local/etc/redis/redis.conf"},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "redis-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "redis-config",
									},
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "redis-data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: storage,
							},
						},
						StorageClassName: &storageClassName,
					},
				},
			},
		},
	}
}

func ptr(s string) *string {
	return &s
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//VolumeSnapshot isn't a native objectt
	if err := volumesnapshotv1.AddToScheme(mgr.GetScheme()); err != nil { //A snapshot controller magare already this kind of resource
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1.Redis{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&volumesnapshotv1.VolumeSnapshot{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
