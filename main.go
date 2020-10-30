package clusterautoscaler

import (
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/yaml"

	"github.com/pulumi/pulumi-aws/sdk/v3/go/aws/iam"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/helm/v2"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi/config"
)

type ClusterAutoscaler struct {
	pulumi.ResourceState

	ID   pulumi.IDOutput `pulumi:"ID"`
	Args Args            `pulumi:"Args"`
}

type Args struct {
	CreateNamespace bool
	Namespace       string
	ClusterName     string
}

func NewClusterAutoscaler(ctx *pulumi.Context, name string, args Args, opts ...pulumi.ResourceOption) (*ClusterAutoscaler, error) {
	ca := &ClusterAutoscaler{}

	autoscalerConfig := config.New(ctx, "clusterautoscaler")
	awsConfig := config.New(ctx, "region")
	oidcArn := autoscalerConfig.Require("oidcArn")
	oidcUrl := autoscalerConfig.Require("oidcUrl")
	region := awsConfig.Require("region")

	var err error

	/*
	 * Policy JSON for IAM role
	 */
	clusterAutoScalerRolePolicyJSON, err := json.Marshal(map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []interface{}{
			map[string]interface{}{
				"Action": []string{
					"autoscaling:DescribeAutoScalingGroups",
					"autoscaling:DescribeAutoScalingInstances",
					"autoscaling:DescribeLaunchConfigurations",
					"autoscaling:DescribeTags",
					"autoscaling:SetDesiredCapacity",
					"autoscaling:TerminateInstanceInAutoScalingGroup",
				},
				"Effect":   "Allow",
				"Resource": "*",
			},
		},
	})

	serviceAccountName := fmt.Sprintf("system:serviceaccount:%s:cluster-autoscaler-aws-cluster-autoscaler-chart", args.Namespace)

	/*
	 * IAM policy principal
	 */
	assumeRolePolicyJSON, _ := json.Marshal(map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []interface{}{
			map[string]interface{}{
				"Effect": "Allow",
				"Principal": map[string]interface{}{
					"Federated": oidcArn,
				},
				"Action": "sts:AssumeRoleWithWebIdentity",
				"Condition": map[string]interface{}{
					"StringEquals": map[string]interface{}{
						fmt.Sprintf("%s:sub", oidcUrl): serviceAccountName,
					},
				},
			},
		},
	})

	/*
	 * Create the IAM role
	 */
	clusterAutoScalerIamRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-iam-role", name), &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(assumeRolePolicyJSON),
	})

	/*
	 * Attach a policy
	 */
	clusterAutoscalerPolicy, err := iam.NewPolicy(ctx, fmt.Sprintf("%s-policy", name), &iam.PolicyArgs{
		Policy: pulumi.String(clusterAutoScalerRolePolicyJSON),
	}, pulumi.Parent(clusterAutoScalerIamRole))
	if err != nil {
		return nil, err
	}

	_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-policy-attachment", name), &iam.RolePolicyAttachmentArgs{
		Role:      clusterAutoScalerIamRole.Name,
		PolicyArn: clusterAutoscalerPolicy.Arn,
	}, pulumi.Parent(clusterAutoscalerPolicy))

	// optionally create a namespace
	if args.CreateNamespace {
		_, err := corev1.NewNamespace(ctx, name, &corev1.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name: pulumi.String(args.Namespace),
			},
		}, pulumi.Parent(ca))
		if err != nil {
			return nil, err
		}
	}

	_, err = helm.NewChart(ctx, "cluster-autoscaler", helm.ChartArgs{
		Chart: pulumi.String("cluster-autoscaler-chart"),
		FetchArgs: &helm.FetchArgs{
			Repo: pulumi.String("https://kubernetes.github.io/autoscaler"),
		},
		Values: pulumi.Map{
			"autoDiscovery": pulumi.Map{
				"clusterName": pulumi.String(args.ClusterName),
			},
			"awsRegion": pulumi.String(region),
		},
		Namespace: pulumi.String(args.Namespace),
		Transformations: []yaml.Transformation{
			func(state map[string]interface{}, opts ...pulumi.ResourceOption) {
				if state["kind"] == "ServiceAccount" {
					metadata := state["metadata"].(map[string]interface{})
					metadata["annotations"] = map[string]interface{}{
						"eks.amazonaws.com/role-arn": clusterAutoScalerIamRole.Arn,
					}
				}
			},
		},
	})

	err = ctx.RegisterComponentResource("jaxxstorm:clusterautoscaler", name, ca, opts...)

	if err != nil {
		return nil, err
	}

	return ca, nil

}
