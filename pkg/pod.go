package pod

import (
	"context" 
	"fmt"
	"log"
	"time"

	guuid "github.com/google/uuid"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/matryer/try"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type PodArgs struct {
	PodNamespace string
	Client       *kubernetes.Clientset
	FileID       string
	Input        string
	Output       string
}

func NewPodArgs(fileId, input, output, podNamespace string)(*PodArgs, error){
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	podArgs := &PodArgs{
		PodNamespace: podNamespace,
		Client:	      client,
		FileID:       fileId,
		Input:        input,
		Output:       output,
	}
	return podArgs, nil
}

func (pa PodArgs) CreatePod() error {
	podSpec := pa.GetPodObject()

	var pod *core.Pod = nil

	err := try.Do(func(attempt int) (bool, error){
		var err error
		pod, err = pa.Client.CoreV1().Pods(pa.PodNamespace).Create(context.TODO(), podSpec, metav1.CreateOptions{}) 

		if err != nil  && attempt < 5{
			time.Sleep((time.Duration(attempt) * 5) * time.Second) // exponential 5 second wait
		}

		return attempt < 5, err // try 5 times
	})

	if err != nil {
		return err
	}

	if err == nil && pod == nil {
		err = fmt.Errorf("Failed to create pod and no error returned")
		return err
	}

	if pod != nil {
		log.Printf("Successfully created Pod")
	}

	return nil
}

func (pa PodArgs) GetPodObject() *core.Pod {
	return &core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rebuild-" + guuid.New().String(),
			Namespace: pa.PodNamespace,
		},
		Spec: core.PodSpec{
			ImagePullSecrets: []core.LocalObjectReference{{Name: "regcred"}},
			RestartPolicy:    core.RestartPolicyNever,
			Volumes: []core.Volume{
				{
					Name: "sourcedir",
					VolumeSource: core.VolumeSource{
						PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
							ClaimName: "glasswallsource-pvc",
						},
					},
				},
				{
					Name: "targetdir",
					VolumeSource: core.VolumeSource{
						PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
							ClaimName: "glasswalltarget-pvc",
						},
					},
				},
			},
			Containers: []core.Container{
				{
					Name:            "rebuild",
					Image:           "glasswallsolutions/icap-request-processing",
					ImagePullPolicy: core.PullIfNotPresent,
					Env: []core.EnvVar{
						{Name: "FILE_ID", Value: pa.FileID},
						{Name: "INPUT_PATH", Value: pa.Input},
						{Name: "OUTPUT_PATH", Value: pa.Output},
					},
					VolumeMounts: []core.VolumeMount{
						{Name: "sourcedir", MountPath: "/input"},
						{Name: "targetdir", MountPath: "/output"},
					},
					Resources: core.ResourceRequirements{
						Limits: core.ResourceList{
							core.ResourceCPU: resource.MustParse("1"),
							core.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
}