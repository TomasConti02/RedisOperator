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
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

var startTimeStr string
var endTimeStr string
var statefulSet *appsv1.StatefulSet
var restarted *appsv1.StatefulSet
var snapshot bool = false
var size int
var pvc bool = false

// RedisReconciler reconciles a Redis object
type RedisReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var (
	counter int        // Contatore globale dei tentativi
	mu      sync.Mutex // Mutex per proteggere il contatore
)

//+kubebuilder:rbac:groups=cache.example.com,resources=redis,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cache.example.com,resources=redis/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cache.example.com,resources=redis/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Redis object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *RedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//STATEFULSET REDIS
	logger := log.FromContext(ctx)
	// Variabili per il calcolo della durata
	// Fetch the Redis instance
	redis := &cachev1.Redis{}
	err := r.Get(ctx, req.NamespacedName, redis)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	csvFilePath := filepath.Join("/home/default/TESI", "outputFinale.csv")
	// Creazione del file
	csvFile, err := os.OpenFile(csvFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Info("FAIL CREAZIONE DEL CSV")
		return ctrl.Result{}, err
	}
	defer csvFile.Close()
	// Define ConfigMap for Redis
	configMap := r.configMapForRedis(redis)
	if err := controllerutil.SetControllerReference(redis, configMap, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	foundConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configMap.Name, Namespace: configMap.Namespace}, foundConfigMap)
	if err != nil && client.IgnoreNotFound(err) == nil {
		err = r.Create(ctx, configMap)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	statefulSet = r.statefulSetForRedis(redis, redis.Spec.Size)
	if err := controllerutil.SetControllerReference(redis, statefulSet, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	foundStatefulSet := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: statefulSet.Name, Namespace: statefulSet.Namespace}, foundStatefulSet)
	if err != nil && client.IgnoreNotFound(err) == nil {
		err = r.Create(ctx, statefulSet)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Inizializza startTime al momento della creazione del StatefulSet
		startTime := time.Now()
		startTimeStr = startTime.Format(time.RFC3339)
		message := "TEMPO DI INIZIO CREAZIONE: " + statefulSet.Name + " at " + startTimeStr
		logger.Info(message)
		size = int(*statefulSet.Spec.Replicas)
		return ctrl.Result{Requeue: true}, nil
	}
	allPodsReady := r.areAllPodsReady(ctx, statefulSet)
	if allPodsReady {
		// Calcola endTime quando tutti i pod sono pronti
		endTime := time.Now()
		endTimeStr = endTime.Format(time.RFC3339)
		startTime, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			logger.Error(err, "Errore nel parsing del timestamp di inizio")
			return ctrl.Result{}, err
		}
		duration := endTime.Sub(startTime)
		seconds := duration.Seconds()
		// Log del tempo di creazione e della durata
		message := "TUTTI I POD SONO IN STATO DI RUNNING, INIZIO: " + startTimeStr + " fine: " + endTimeStr + " DURATA DEL CARICAMENTO : " + fmt.Sprintf("%.2f", seconds) + " secondi"
		logger.Info(message)
		//SCRIVIAMO IL RISULTATO DELNTRO IL CSV
		writer := csv.NewWriter(csvFile)
		mu.Lock()
		counter++
		mu.Unlock()
		record := []string{
			strconv.Itoa(counter),      // Converti il numero di tentativi in stringa
			fmt.Sprintf("%f", seconds), // Converti il tempo di elaborazione in stringa
		}
		err = writer.Write(record)
		if err != nil {
			logger.Info("abbiamo scritto")
			return ctrl.Result{}, err
		}
		writer.Flush()
		//GESTIAMO GLI SNAPSHOT DEL SISTEMA
		time.Sleep(120 * time.Second)
		logger.Info(" CREAIMO GLI SNAPSHOT ")
		if !snapshot {
			if err := r.createVolumeSnapshots(redis); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create volume snapshots: %v", err)
			}
		}
		snapshot = true
		time.Sleep(10 * time.Second)
		if !pvc {
			logger.Info(" CREAIMO I PVC ")
			if err := CreatePVCs(r.Client, redis.Namespace, "redis-data-redis-test", redis.Spec.Size, "redis-snapshot", "longhorn-new"); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create PVCs: %v", err)
			}
			pvc = true
			time.Sleep(40 * time.Second)
		}
		logger.Info(" CREAIMO IL RIPRISTINO ")
		restarted = r.statefulSetForRedisRestarted(redis, redis.Spec.Size)
		if err := controllerutil.SetControllerReference(redis, restarted, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info(" CREIAMO IL RESTARTED ")
		foundStatefulSetRestarted := &appsv1.StatefulSet{}
		err = r.Get(ctx, types.NamespacedName{Name: restarted.Name, Namespace: restarted.Namespace}, foundStatefulSetRestarted)
		if err != nil && client.IgnoreNotFound(err) == nil {
			err = r.Create(ctx, restarted)
			size = size * 2
			if err != nil {
				return ctrl.Result{}, err
			}
			startTime := time.Now()
			startTimeStr = startTime.Format(time.RFC3339)
			message := "TEMPO DI INIZIO CREAZIONE RESTARTED: " + statefulSet.Name + " at " + startTimeStr
			logger.Info(message)
			// Inizializza startTime al momento della creazione del StatefulSet
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

func CreatePVCs(c client.Client, namespace string, baseName string, numPVCs int32, snapshotName string, storageClassName string) error {
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

		if err := c.Create(context.Background(), pvc); err != nil {
			return err
		}
	}
	return nil
}

func (r *RedisReconciler) statefulSetForRedisRestarted(redis *cachev1.Redis, size int32) *appsv1.StatefulSet {
	// Definisci le etichette per il selettore e i pod
	labels := map[string]string{
		"app": redis.Name,
	}

	// Definisci le risorse di storage e lo storage class
	storage := resource.MustParse("1Gi")
	storageClassName := "longhorn-new"

	// Crea un template di PVC per il StatefulSet
	volumeClaimTemplates := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis-data", // Nome del PVC come specificato nel manifest YAML
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
				// Nota: nel manifest YAML non viene specificato il DataSource, quindi non è incluso
			},
		},
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-test", // Nome del StatefulSet
			Namespace: redis.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "redis-test-service", // Nome del servizio associato al StatefulSet
			Replicas:    &size,                // Numero di repliche
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
									Name:          "redis", // Nome della porta come nel manifest YAML
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "redis-data", // Nome del volume mount
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

func isPodReady(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning

}
func (r *RedisReconciler) areAllPodsReady(ctx context.Context, statefulSet *appsv1.StatefulSet) bool {
	pods := &corev1.PodList{}
	//labelSelector := labels.SelectorFromSet(statefulSet.Spec.Selector.MatchLabels)
	listOps := &client.ListOptions{
		Namespace: "default",
		/*
			Namespace:     statefulSet.Namespace,
			LabelSelector: labelSelector,*/
	}
	if err := r.List(ctx, pods, listOps); err != nil {
		return false
	}
	// vediamo se ci sono tutti
	if len(pods.Items) != size {
		return false
	}
	// vediamo se tutti sono pronti in stato running
	for _, pod := range pods.Items {
		if !isPodReady(&pod) {
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

func (r *RedisReconciler) createVolumeSnapshots(redis *cachev1.Redis) error {
	size := redis.Spec.Size
	for i := int32(0); i < size; i++ {
		pvcName := fmt.Sprintf("redis-data-redis-cluster-%d", i) // Nome del PVC da cui fare lo snapshot
		snapshotName := fmt.Sprintf("redis-snapshot-%d", i)

		snapshot := &volumesnapshotv1.VolumeSnapshot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      snapshotName,
				Namespace: redis.Namespace, // Usa lo stesso namespace del Redis CRD
			},
			Spec: volumesnapshotv1.VolumeSnapshotSpec{
				VolumeSnapshotClassName: ptr("new-volumesnapshotclass-longhorn"),
				Source: volumesnapshotv1.VolumeSnapshotSource{
					PersistentVolumeClaimName: ptr(pvcName),
				},
			},
		}

		// Crea il VolumeSnapshot
		if err := r.Client.Create(context.Background(), snapshot); err != nil {
			return fmt.Errorf("error creating VolumeSnapshot %s: %v", snapshotName, err)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := volumesnapshotv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1.Redis{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&volumesnapshotv1.VolumeSnapshot{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

/*// Define the VolumeSnapshot
volumeSnapshot := &volumesnapshotv1.VolumeSnapshot{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "example-snapshot",
		Namespace: redis.Namespace, // Assume Redis and VolumeSnapshot are in the same namespace
	},
	Spec: volumesnapshotv1.VolumeSnapshotSpec{
		VolumeSnapshotClassName: pointer.StringPtr("csi-volume-snapshot-class"), // Replace with your VolumeSnapshotClass
		Source: volumesnapshotv1.VolumeSnapshotSource{
			PersistentVolumeClaimName: pointer.StringPtr("your-pvc-name"), // Replace with your PVC name
		},
	},
}*/
