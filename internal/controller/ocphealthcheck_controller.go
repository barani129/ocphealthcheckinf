/*
Copyright 2025 baranitharan.chittharanjan@spark.co.nz.

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
	"os"
	"sync"
	"time"

	gcruntime "runtime"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	"github.com/barani129/ocphealthcheckinf/internal/hubutil"
	"github.com/barani129/ocphealthcheckinf/internal/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// OcpHealthCheckReconciler reconciles a OcpHealthCheck object
type OcpHealthCheckReconciler struct {
	client.Client
	RESTClient               rest.Interface
	RESTConfig               *rest.Config
	Kind                     string
	ClusterResourceNamespace string
	recorder                 record.EventRecorder
	Scheme                   *runtime.Scheme
	InformerCount            int64
	InformerTimer            *metav1.Time
	mu                       sync.Mutex
	factory                  dynamicinformer.DynamicSharedInformerFactory
	PodInformer              cache.SharedIndexInformer
	MCPInformer              cache.SharedIndexInformer
	NodeInformer             cache.SharedIndexInformer
	PolicyInformer           cache.SharedIndexInformer
	CoInformer               cache.SharedIndexInformer
	NNCPInformer             cache.SharedIndexInformer
	CatalogSourceInformer    cache.SharedIndexInformer
	CsvInformer              cache.SharedIndexInformer
	TunedInformer            cache.SharedIndexInformer
	TridentInformer          cache.SharedIndexInformer
	stopChan                 chan struct{}
	podCleaner               *metav1.Time
}

func (r *OcpHealthCheckReconciler) newOcpHealthChecker() (client.Object, error) {
	OcpHealthcheckKind := ocphealthcheckv1.GroupVersion.WithKind(r.Kind)
	ro, err := r.Scheme.New(OcpHealthcheckKind)
	if err != nil {
		return nil, err
	}
	return ro.(client.Object), nil
}

// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="machineconfiguration.openshift.io",resources=machineconfigpools,verbs=get;list;watch
// +kubebuilder:rbac:groups="config.openshift.io",resources=clusteroperators,verbs=get;list;watch
// +kubebuilder:rbac:groups="operators.coreos.com",resources=catalogsources,verbs=get;list;watch
// +kubebuilder:rbac:groups="operators.coreos.com",resources=clusterserviceversions,verbs=get;list;watch
// +kubebuilder:rbac:groups="nmstate.io",resources=nodenetworkconfigurationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="policy.open-cluster-management.io",resources=policies,verbs=get;list;watch
// +kubebuilder:rbac:groups="cluster.open-cluster-management.io",resources=managedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="argoproj.io",resources=argocds,verbs=get;list;watch
// +kubebuilder:rbac:groups="tuned.openshift.io",resources=profiles,verbs=get;list;watch
// +kubebuilder:rbac:groups="trident.netapp.io",resources=tridentbackendconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="trident.netapp.io",resources=tridentbackends,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OcpHealthCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *OcpHealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	_ = log.FromContext(ctx)

	ocpScan, err := r.newOcpHealthChecker()
	if err != nil {
		log.Log.Error(err, "unrecognized ocphealthcheck type")
		return ctrl.Result{}, err
	}
	if err = r.Get(ctx, req.NamespacedName, ocpScan); err != nil {
		if err := client.IgnoreNotFound(err); err != nil {
			return ctrl.Result{}, fmt.Errorf("unable to retrieve OcpHealthCheck")
		}
		log.Log.Info("OcpHealthCheck is not found")
		return ctrl.Result{}, nil
	}
	spec, status, err := util.GetSpecAndStatus(ocpScan)
	if err != nil {
		log.Log.Error(err, "unable to retrieve OcpHealthCheck spec and status")
		return ctrl.Result{}, err
	}

	if spec.Suspend != nil && *spec.Suspend {
		log.Log.Info("OcpHealthCheck is suspended, skipping...")
		return ctrl.Result{}, nil
	}

	if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
		if spec.Email == "" || spec.RelayHost == "" {
			return ctrl.Result{}, fmt.Errorf("please configure valid email address/relay host in spec")
		}
	}

	// switch ocpScan.(type) {
	// case *ocphealthcheckv1.OcpHealthCheck:
	// 	// do nothing
	// default:
	// 	log.Log.Error(fmt.Errorf("unexpected ocphealthcheckscan object type: %s", ocpScan), "not retrying")
	// 	return ctrl.Result{}, nil
	// }

	// report gives feedback by updating the Ready condition of the ocphealthcheck scan
	report := func(conditionStatus ocphealthcheckv1.ConditionStatus, message string, err error) {
		eventType := corev1.EventTypeNormal
		if err != nil {
			log.Log.Error(err, message)
			eventType = corev1.EventTypeWarning
			message = fmt.Sprintf("%s: %v", message, err)
		} else {
			log.Log.Info(message)
		}
		r.recorder.Event(ocpScan, eventType, ocphealthcheckv1.EventReasonIssuerReconciler, message)
		util.SetReadyCondition(status, conditionStatus, ocphealthcheckv1.EventReasonIssuerReconciler, message)
	}

	defer func() {
		if err != nil {
			report(ocphealthcheckv1.ConditionFalse, "Trouble running OcpHealthCheckScan", err)
		}
		if updateErr := r.Status().Update(ctx, ocpScan); updateErr != nil {
			err = utilerrors.NewAggregate([]error{err, updateErr})
			result = ctrl.Result{}
		}
	}()

	if readyCond := util.GetReadyCondition(status); readyCond == nil {
		report(ocphealthcheckv1.ConditionUnknown, "First Seen", nil)
		return ctrl.Result{}, nil
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return ctrl.Result{}, err
	}

	staticClientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ctrl.Result{}, err
	}

	var runningHost string
	domain, err := util.GetAPIName(*staticClientSet)
	if err == nil && domain == "" {
		if spec.Cluster != nil {
			runningHost = *spec.Cluster
		}
	} else if err == nil && domain != "" {
		runningHost = domain
	} else {
		log.Log.Error(err, "unable to retrieve ocp config")
		runningHost = "local-cluster"
	}

	clientset, err := dynamic.NewForConfig(config)
	if err != nil {
		return ctrl.Result{}, err
	}
	podResource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	nodeResource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "nodes",
	}

	mcpResource := schema.GroupVersionResource{
		Group:    "machineconfiguration.openshift.io",
		Version:  "v1",
		Resource: "machineconfigpools",
	}

	policyResource := schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}

	coResource := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusteroperators",
	}

	nncpResource := schema.GroupVersionResource{
		Group:    "nmstate.io",
		Version:  "v1",
		Resource: "nodenetworkconfigurationpolicies",
	}

	catalogResource := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "catalogsources",
	}

	csvResource := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "clusterserviceversions",
	}

	tpResource := schema.GroupVersionResource{
		Group:    "tuned.openshift.io",
		Version:  "v1",
		Resource: "profiles",
	}
	tridentResource := schema.GroupVersionResource{
		Group:    "trident.netapp.io",
		Version:  "v1",
		Resource: "tridentbackends",
	}
	if spec.HubCluster != nil && *spec.HubCluster {
		log.Log.Info("Running MCP Checks")
		if inProgress, err := util.CheckMCPINProgress(staticClientSet); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute * 30}, err
		} else if inProgress {
			log.Log.Info("MCP update is in progress, exiting")
			return ctrl.Result{RequeueAfter: time.Minute * 30}, fmt.Errorf("mcp is in progress")
		}
		hubutil.OnMCPUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running Pod Checks")
		hubutil.OnPodUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running Node Checks")
		hubutil.OnNodeUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running Policy Checks")
		hubutil.OnPolicyUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running ClusterOperator Checks")
		hubutil.OnCoUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running NNCP Checks")
		hubutil.OnNNCPUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running CatalogSource Checks")
		hubutil.OnCatalogSourceUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running ClusterServiceVersion Checks")
		hubutil.OnCsvUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running ArgoCD Checks")
		hubutil.OnArgoUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running ManagedCluster Checks")
		hubutil.OnManagedClusterUpdate(staticClientSet, spec, runningHost)
		log.Log.Info("Running Spark cluster issuer checks")
		hubutil.CheckAppviewx(staticClientSet, spec, runningHost)
		log.Log.Info("Running pod cleanup")
		util.CleanUpRunningPods(staticClientSet, spec, runningHost)
		log.Log.Info("Checking HP CSI backend reachability")
		util.CheckHPEBackendConnectivity(staticClientSet, spec, runningHost)
		if files, err := os.ReadDir("/home/golanguser/files/ocphealth/"); err != nil {
			log.Log.Error(err, "unable to read files")
		} else if len(files) > 0 {
			status.Healthy = false
		} else {
			status.Healthy = true
		}
		report(ocphealthcheckv1.ConditionTrue, "hub healthcheck functions compiled successfully", nil)
		return ctrl.Result{Requeue: true}, nil
	} else {
		r.mu.Lock()
		defer r.mu.Unlock()
		pastTime := time.Now().Add(-1 * time.Minute * 3)
		podCleanupTime := time.Now().Add(-1 * time.Hour * 1)
		if r.factory != nil {
			if r.InformerTimer != nil && r.InformerTimer.Time.Before(pastTime) {
				log.Log.Info("closing the factory")
				close(r.stopChan)
				r.PodInformer = nil
				r.NNCPInformer = nil
				r.NodeInformer = nil
				r.MCPInformer = nil
				r.PolicyInformer = nil
				r.CatalogSourceInformer = nil
				r.CsvInformer = nil
				r.CoInformer = nil
				r.TunedInformer = nil
				r.stopChan = nil
				r.factory = nil
				gcruntime.GC()
				r.stopChan = make(chan struct{})
				r.factory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientset, time.Hour*10, corev1.NamespaceAll, nil)
				r.MCPInformer = r.factory.ForResource(mcpResource).Informer()
				r.PodInformer = r.factory.ForResource(podResource).Informer()
				r.NodeInformer = r.factory.ForResource(nodeResource).Informer()
				r.PolicyInformer = r.factory.ForResource(policyResource).Informer()
				r.CoInformer = r.factory.ForResource(coResource).Informer()
				r.NNCPInformer = r.factory.ForResource(nncpResource).Informer()
				r.CatalogSourceInformer = r.factory.ForResource(catalogResource).Informer()
				r.CsvInformer = r.factory.ForResource(csvResource).Informer()
				r.TunedInformer = r.factory.ForResource(tpResource).Informer()
				r.TridentInformer = r.factory.ForResource(tridentResource).Informer()
				r.MCPInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnMCPUpdate(newObj, staticClientSet, status, spec, runningHost)
					},
				})
				log.Log.Info("Adding add pod events to pod informer")
				r.PodInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj interface{}, newObj interface{}) {
						util.OnPodUpdate(newObj, spec, status, runningHost, staticClientSet)
					},
					DeleteFunc: func(obj interface{}) {
						util.OnPodDelete(obj, staticClientSet, spec, status, runningHost)
					},
				})
				r.NodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnNodeUpdate(newObj, spec, status, runningHost)
					},
				})
				r.PolicyInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnPolicyUpdate(newObj, staticClientSet, spec, status, runningHost)
					},
					DeleteFunc: func(obj interface{}) {
						util.OnPolicyDelete(obj, spec, status, runningHost)
					},
				})
				r.CoInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnCoUpdate(newObj, staticClientSet, spec, runningHost)
					},
				})
				r.CatalogSourceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnCatalogSourceUpdate(newObj, staticClientSet, spec, runningHost)
					},
				})
				r.NNCPInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnNNCPUpdate(newObj, staticClientSet, spec, runningHost)
					},
				})
				r.CsvInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnCsvUpdate(newObj, staticClientSet, spec, runningHost)
					},
				})
				r.TunedInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnTunedProfileUpdate(newObj, spec, runningHost)
					},
				})
				r.TridentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
					UpdateFunc: func(oldObj, newObj interface{}) {
						util.OnTridentBackendUpdate(newObj, spec, runningHost)
					},
				})
				log.Log.Info("Starting dynamic informer factory again")
				now := metav1.Now()
				r.InformerTimer = &now
				go r.factory.Start(r.stopChan)
				report(ocphealthcheckv1.ConditionTrue, "dynamic informers compiled successfully", nil)
				log.Log.Info("Checking trident backend management LIF reachability")
				util.CheckTridentBackendConnectivity(staticClientSet, spec, runningHost)
				log.Log.Info("Checking EVFNM reachability")
				util.CheckEVNFMConnectivity(spec, runningHost)
				if r.podCleaner.Time.Before(podCleanupTime) {
					log.Log.Info("Running pod files cleanup")
					util.CleanUpRunningPods(staticClientSet, spec, runningHost)
					log.Log.Info("Completed pod files cleanup")
					now := metav1.Now()
					r.podCleaner = &now
				}
				if files, err := os.ReadDir("/home/golanguser/files/ocphealth/"); err != nil {
					log.Log.Info(err.Error())
				} else if len(files) > 0 {
					status.Healthy = false
				} else {
					status.Healthy = true
				}
			}
		} else {
			r.stopChan = make(chan struct{})
			r.factory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientset, time.Hour*10, corev1.NamespaceAll, nil)
			r.MCPInformer = r.factory.ForResource(mcpResource).Informer()
			r.PodInformer = r.factory.ForResource(podResource).Informer()
			r.NodeInformer = r.factory.ForResource(nodeResource).Informer()
			r.PolicyInformer = r.factory.ForResource(policyResource).Informer()
			r.CoInformer = r.factory.ForResource(coResource).Informer()
			r.NNCPInformer = r.factory.ForResource(nncpResource).Informer()
			r.CatalogSourceInformer = r.factory.ForResource(catalogResource).Informer()
			r.CsvInformer = r.factory.ForResource(csvResource).Informer()
			r.TunedInformer = r.factory.ForResource(tpResource).Informer()
			r.TridentInformer = r.factory.ForResource(tridentResource).Informer()
			r.MCPInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnMCPUpdate(newObj, staticClientSet, status, spec, runningHost)
				},
			})
			log.Log.Info("Adding add pod events to pod informer")
			r.PodInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj interface{}, newObj interface{}) {
					util.OnPodUpdate(newObj, spec, status, runningHost, staticClientSet)
				},
				DeleteFunc: func(obj interface{}) {
					util.OnPodDelete(obj, staticClientSet, spec, status, runningHost)
				},
			})
			r.NodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnNodeUpdate(newObj, spec, status, runningHost)
				},
			})
			r.PolicyInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnPolicyUpdate(newObj, staticClientSet, spec, status, runningHost)
				},
				DeleteFunc: func(obj interface{}) {
					util.OnPolicyDelete(obj, spec, status, runningHost)
				},
			})
			r.CoInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnCoUpdate(newObj, staticClientSet, spec, runningHost)
				},
			})
			r.CatalogSourceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnCatalogSourceUpdate(newObj, staticClientSet, spec, runningHost)
				},
			})
			r.NNCPInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnNNCPUpdate(newObj, staticClientSet, spec, runningHost)
				},
			})
			r.CsvInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnCsvUpdate(newObj, staticClientSet, spec, runningHost)
				},
			})
			r.TunedInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnTunedProfileUpdate(newObj, spec, runningHost)
				},
			})
			r.TridentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj interface{}) {
					util.OnTridentBackendUpdate(newObj, spec, runningHost)
				},
			})
			log.Log.Info("Starting dynamic informer factory")
			now := metav1.Now()
			r.InformerTimer = &now
			r.podCleaner = &now
			go r.factory.Start(r.stopChan)
			log.Log.Info("Checking trident backend management LIF reachability")
			util.CheckTridentBackendConnectivity(staticClientSet, spec, runningHost)
			log.Log.Info("Checking EVFNM reachability")
			util.CheckEVNFMConnectivity(spec, runningHost)
			report(ocphealthcheckv1.ConditionTrue, "dynamic informers compiled successfully", nil)
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *OcpHealthCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor(ocphealthcheckv1.EventSource)
	return ctrl.NewControllerManagedBy(mgr).
		For(&ocphealthcheckv1.OcpHealthCheck{}).
		Named("ocphealthcheck").
		Complete(r)
}
