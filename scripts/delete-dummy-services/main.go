package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	kubeconfig *string
	namespace  *string
)

func main() {
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	namespace = flag.String("namespace", "default", "namespace for the services")
	flag.Parse()

	fmt.Println("Deleting all dummy services and endpointslices...")
	startTime := time.Now()

	// Build Kubernetes client
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// Increase rate limits for high-throughput operations
	config.QPS = 100   // requests per second
	config.Burst = 200 // burst capacity

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Delete services with label type=dummy
	fmt.Println("Deleting services with label type=dummy...")
	err = clientset.CoreV1().Services(*namespace).DeleteCollection(
		context.Background(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: "type=dummy"},
	)
	if err != nil {
		fmt.Printf("Error deleting services: %v\n", err)
	}

	// Delete endpointslices with label type=dummy
	fmt.Println("Deleting endpointslices with label type=dummy...")
	err = clientset.DiscoveryV1().EndpointSlices(*namespace).DeleteCollection(
		context.Background(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: "type=dummy"},
	)
	if err != nil {
		fmt.Printf("Error deleting endpointslices: %v\n", err)
	}

	duration := time.Since(startTime)
	fmt.Printf("\n============================================\n")
	fmt.Printf(" Deletion Complete in %v\n", duration)
	fmt.Printf("============================================\n")
	fmt.Println("All dummy services and endpointslices deleted.")
	fmt.Println("")
}
