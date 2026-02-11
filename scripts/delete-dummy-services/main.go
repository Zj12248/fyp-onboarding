package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"sync"
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

	// Delete services with label type=dummy (using List + Delete since DeleteCollection not available)
	fmt.Println("Deleting services with label type=dummy...")
	serviceList, err := clientset.CoreV1().Services(*namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: "type=dummy"},
	)
	if err != nil {
		fmt.Printf("Error listing services: %v\n", err)
	} else {
		// Use concurrency to delete services quickly (matching client Burst)
		count := len(serviceList.Items)
		fmt.Printf("Found %d services to delete. Starting parallel deletion...\n", count)

		var wg sync.WaitGroup
		// Limit concurrency to avoid overwhelming the client or system resources
		// The client rate limiter (QPS=100) will still throttle us appropriately
		semaphore := make(chan struct{}, 50)

		for _, svc := range serviceList.Items {
			wg.Add(1)
			semaphore <- struct{}{} // Acquire token

			go func(name string) {
				defer wg.Done()
				defer func() { <-semaphore }() // Release token

				err := clientset.CoreV1().Services(*namespace).Delete(
					context.Background(),
					name,
					metav1.DeleteOptions{},
				)
				if err != nil {
					// Ignore "not found" errors which happen if deletion races
					fmt.Printf("Error deleting service %s: %v\n", name, err)
				}
			}(svc.Name)
		}

		wg.Wait()
		fmt.Printf("Deleted %d services.\n", count)
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
