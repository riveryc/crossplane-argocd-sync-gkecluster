package main

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"os"
	"sigs.k8s.io/yaml"
	"strings"
)

// API client for managing secrets
//var secretsClient coreV1Types.SecretInterface

type TLSClientConfig struct {
	Insecure bool   `json:"insecure"`
	CaData   string `json:"caData"`
}

type AuthConfig struct {
	ClusterName string `json:"clusterName"`
}

type ArgoConfig struct {
	BearerToken	string	`json:"bearerToken"`
	TLSClientConfig TLSClientConfig `json:"tlsClientConfig"`
	AuthConfig   AuthConfig   `json:"AuthConfig"`
}

type KubeConfig struct {
	APIVersion string `json:"apiVersion"`
	Clusters   []struct {
		Cluster struct {
			CertificateAuthorityData string `json:"certificate-authority-data"`
			Server                   string `json:"server"`
		} `json:"cluster"`
		Name string `json:"name"`
	} `json:"clusters"`
	Contexts []struct {
		Context struct {
			Cluster string `json:"cluster"`
			User    string `json:"user"`
		} `json:"context"`
		Name string `json:"name"`
	} `json:"contexts"`
	CurrentContext string `json:"current-context"`
	Kind           string `json:"kind"`
	Preferences    struct {
	} `json:"preferences"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Token string `json:"token"`
		} `json:"user"`
	} `json:"users"`
}

func main() {
	log.Print("Starting monitoring...")

	// connect to Kubernetes API
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// set api clients up
	// kubernetes core api
	clientsetCore, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// listen for new secrets
	factory := kubeinformers.NewSharedInformerFactoryWithOptions(clientsetCore, 0)
	informer := factory.Core().V1().Secrets().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(new interface{}) {
			// get secret
			var secret = new.(*v1.Secret).DeepCopy()
			for _, o := range secret.OwnerReferences{
				if o.Kind == "GKECluster" {
					log.Printf("Found secret for cluster: %s", o.Name)

					// prepare argo config
					argoConfig := ArgoConfig{}
					var server string

					// extract data from crossplane secret
					var data = *&secret.Data
					for k, v := range data {
						switch k {
						case "kubeconfig":
							var kubeConfig KubeConfig
							err := yaml.Unmarshal(v, &kubeConfig)
							if err != nil {
								fmt.Println(err)
								return
							}
							// The context is named after the clustername
							argoConfig.AuthConfig.ClusterName = kubeConfig.CurrentContext
						case "clusterCA":
							b64 := b64.StdEncoding.EncodeToString(v)
							argoConfig.TLSClientConfig.CaData = b64
							argoConfig.TLSClientConfig.Insecure = false
						case "endpoint":
							server = string(v)
						}
					}
					argoConfigJSON, err := json.Marshal(argoConfig)
					if err != nil {
						fmt.Println(err)
						return
					}

					// clustername needs to be in this specific format to be accepted by argo
					// gke_projectID_region_name
					// (actually not sure about it, read a comment on github)
					var argoClusterName string = "gke-" + argoConfig.AuthConfig.ClusterName
					// argoClusterName := argoConfig.AwsAuthConfig.ClusterName

					// write kubernetes secret to argocd namespace
					// (so that argocd picks it up as a cluster)
					secret := v1.Secret{
						TypeMeta: metav1.TypeMeta{
							Kind:       "Secret",
							APIVersion: "v1",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      namespace() + "-" + argoConfig.AuthConfig.ClusterName,
							Namespace: "argocd",
							Annotations: map[string]string{
								"managed-by": "argocd.argoproj.io",
							},
							Labels: map[string]string{
								"argocd.argoproj.io/secret-type": "cluster",
							},
						},
						Data: map[string][]byte{
							"config": []byte(argoConfigJSON),
							"name":   []byte(argoClusterName),
							"server": []byte(server),
						},
						Type: "Opaque",
					}
					log.Print(secret)

					//secretOut, err := clientsetCore.CoreV1().Secrets("argocd").Create(&secret)
					//if err != nil {
					//	fmt.Println(err)
					//} else {
					//	fmt.Println("Added cluster", secretOut.GetName())
					//}

					// initial argo project
					//argoProject := argo_v1alpha1.AppProject{
					//	TypeMeta: metav1.TypeMeta{
					//		Kind:       "AppProject",
					//		APIVersion: "argoproj.io/v1alpha1",
					//	},
					//	ObjectMeta: metav1.ObjectMeta{
					//		Name: namespace() + "-" + argoConfig.AuthConfig.ClusterName,
					//	},
					//	Spec: argo_v1alpha1.AppProjectSpec{
					//		Description: argoConfig.AuthConfig.ClusterName + " EKS cluster owned by " + namespace(),
					//		Destinations: []argo_v1alpha1.ApplicationDestination{
					//			argo_v1alpha1.ApplicationDestination{
					//				Namespace: "istio-system",
					//				Server:    server,
					//			},
					//			argo_v1alpha1.ApplicationDestination{
					//				Namespace: "istio-operator",
					//				Server:    server,
					//			},
					//			argo_v1alpha1.ApplicationDestination{
					//				Namespace: "styra-system",
					//				Server:    server,
					//			},
					//			argo_v1alpha1.ApplicationDestination{
					//				Namespace: "knative-serving",
					//				Server:    server,
					//			},
					//			argo_v1alpha1.ApplicationDestination{
					//				Namespace: "serving-operator",
					//				Server:    server,
					//			},
					//		},
					//		ClusterResourceWhitelist: []metav1.GroupKind{
					//			metav1.GroupKind{
					//				Group: "*",
					//				Kind:  "*",
					//			},
					//		},
					//		SourceRepos: []string{"https://github.com/janwillies/gitops-manifests-private"},
					//		// OrphanedResources: &argo_v1alpha1.OrphanedResourcesMonitorSettings{},
					//	},
					//}
					//argoProjectOut, err := clientsetArgo.ArgoprojV1alpha1().AppProjects("argocd").Create(&argoProject)
					//if err != nil {
					//	fmt.Println(err)
					//} else {
					//	fmt.Println("Added project", argoProjectOut.GetName())
					//}

					// intial argo application
					//argoApplication := argo_v1alpha1.Application{
					//	TypeMeta: metav1.TypeMeta{
					//		// Kind:       argo_v1alpha1.ApplicationSchemaGroupVersionKind.String(),
					//		// APIVersion: argo_v1alpha1.AppProjectSchemaGroupVersionKind.GroupVersion().Identifier(),
					//		Kind:       "Application",
					//		APIVersion: "argoproj.io/v1alpha1",
					//	},
					//	ObjectMeta: metav1.ObjectMeta{
					//		Name: "infra-" + namespace() + "-" + argoConfig.AuthConfig.ClusterName,
					//		// Finalizers: []string{"resources-finalizer.argocd.argoproj.io"},
					//	},
					//	Spec: argo_v1alpha1.ApplicationSpec{
					//		Project: namespace() + "-" + argoConfig.AuthConfig.ClusterName,
					//		Destination: argo_v1alpha1.ApplicationDestination{
					//			Namespace: "styra-system",
					//			Server:    server,
					//		},
					//		// Source: argo_v1alpha1.ApplicationSource{
					//		// 	RepoURL:        "https://github.com/janwillies/gitops-manifests-private",
					//		// 	Path:           "user-infra",
					//		// 	TargetRevision: "HEAD",
					//		// },
					//		Source: argo_v1alpha1.ApplicationSource{
					//			RepoURL:        "https://github.com/janwillies/gitops-manifests-private",
					//			Path:           "user-infra",
					//			TargetRevision: "HEAD",
					//		},
					//		SyncPolicy: &argo_v1alpha1.SyncPolicy{
					//			Automated: &argo_v1alpha1.SyncPolicyAutomated{
					//				Prune:    true,
					//				SelfHeal: true,
					//			},
					//		},
					//	},
					//}
					//argoApplicationOut, err := clientsetArgo.ArgoprojV1alpha1().Applications("argocd").Create(&argoApplication)
					//if err != nil {
					//	fmt.Println(err)
					//} else {
					//	fmt.Println("Added application", argoApplicationOut.GetName())
					//}

				}
			}
		},
	})

	informer.Run(stopper)
}

// functions
func namespace() string {
	// This way assumes you've set the POD_NAMESPACE environment variable using the downward API.
	// This check has to be done first for backwards compatibility with the way InClusterConfig was originally set up
	if ns, ok := os.LookupEnv("POD_NAMESPACE"); ok {
		return ns
	}

	// Fall back to the namespace associated with the service account token, if available
	if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return "default"
}