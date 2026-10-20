# Setting up a jumphost

airlock tunnels to private resources through one small EC2 instance per AWS account and region: the
jumphost. It needs no SSH, no key pair, no public IP and no inbound rule: the SSM Agent on it
reaches out to AWS Systems Manager, and Session Manager relays the port forwarding through that
connection.

Values in angle brackets are placeholders: `<region>`, `<account-id>`, `<vpc-id>`,
`<private-subnet-id>`, and so on. Replace them with your own; keep real account IDs, instance names
and tags out of shared documents.

## 1. Network

Place the jumphost in a **private subnet** of the VPC hosting your targets, in a subnet that can
route to them.

It must reach Systems Manager over HTTPS, either:

- through a **NAT gateway** (outbound internet), or
- without internet access, through **VPC interface endpoints** for `com.amazonaws.<region>.ssm`,
  `com.amazonaws.<region>.ssmmessages` and `com.amazonaws.<region>.ec2messages`, with private DNS
  enabled and a security group allowing HTTPS (443) from the jumphost.

Security groups:

| Security group | Rule | Port | Peer |
|---|---|---|---|
| jumphost | inbound | none | — |
| jumphost | outbound | 443 | Systems Manager (NAT or the VPC endpoints' security group) |
| jumphost | outbound | the targets' ports: 5432 (PostgreSQL), 3306 (MySQL), 1433 (SQL Server), 6379 (Redis / Valkey), 443 (EKS API) | the targets' security groups |
| each target | inbound | its port | the jumphost security group |

Referencing security groups rather than CIDR ranges keeps the rules valid when addresses change.

## 2. Instance profile

Create an IAM role for EC2 with the AWS managed policy **`AmazonSSMManagedInstanceCore`**, and
attach it to the instance as its instance profile. That is all the jumphost needs: it only relays
TCP traffic, it never calls the target services' APIs.

```bash
aws iam create-role --role-name jumphost-ssm \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam attach-role-policy --role-name jumphost-ssm \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam create-instance-profile --instance-profile-name jumphost-ssm
aws iam add-role-to-instance-profile --instance-profile-name jumphost-ssm --role-name jumphost-ssm
```

## 3. Instance

Any small instance with the SSM Agent works; Amazon Linux 2023 ships with it. A `t4g.nano` (ARM)
or `t3.nano` is enough: the tunnels are light.

```bash
aws ec2 run-instances --region <region> \
  --image-id resolve:ssm:/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64 \
  --instance-type t4g.nano \
  --subnet-id <private-subnet-id> \
  --security-group-ids <jumphost-security-group-id> \
  --iam-instance-profile Name=jumphost-ssm \
  --no-associate-public-ip-address \
  --metadata-options HttpTokens=required \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=jumphost},{Key=Usage,Value=jumphost}]'
```

- **The tag** is how airlock finds the instance: it looks for a *running* instance tagged
  `Usage=jumphost`. To use another tag, set it once with
  `airlock config set aws.jumphostTag <Key>=<Value>` (see the README's Configuration section) and share
  it with `airlock config export`.
- No key pair and no SSH: Session Manager replaces them.
- `HttpTokens=required` enforces IMDSv2.

After a few minutes the instance registers with Systems Manager:

```bash
aws ssm describe-instance-information --region <region> \
  --query 'InstanceInformationList[].{id:InstanceId,ping:PingStatus,agent:AgentVersion}'
```

`PingStatus` must be `Online`. If the instance does not show up, check the instance profile and the
route to Systems Manager (step 1).

## 4. Targets

- **RDS**: allow the database port from the jumphost security group. To sign in with AWS IAM
  instead of passwords, enable IAM database authentication on the instance and create the database
  user granted `rds_iam` (PostgreSQL) or identified with `AWSAuthenticationPlugin` (MySQL).
- **EKS**: use a private (or public and private) cluster endpoint, and allow 443 from the jumphost
  security group in the cluster security group. Developers still need Kubernetes access (an EKS
  access entry or an `aws-auth` mapping for their IAM role). The API certificate is issued for the
  cluster endpoint's host name, so point `kubectl` at the tunnel with it as TLS server name:
  `kubectl --server https://localhost:<local-port> --tls-server-name <cluster-endpoint-host> ...`,
  or set `tls-server-name` in the kubeconfig cluster entry.
- **ElastiCache**: allow the cache port from the jumphost security group. With in-transit
  encryption, connect with TLS and the endpoint's host name as SNI.
- **SQL Server read-only access for the MCP server** (optional): tag the per-database read-only
  Secrets Manager secrets with one tag and set it with
  `airlock config set modules.sql.readOnlySecretTag <Key>=<Value>`.

## 5. Developer permissions

Developers need an IAM policy like the one below (one statement per purpose; drop the ones you do
not use), and, for the tunnels through the jumphost (RDS, EKS, ElastiCache), the AWS CLI v2 and the
Session Manager plugin. Scope `Resource` to your accounts and regions.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "FindJumphostAndTargets",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeInstances",
        "rds:DescribeDBInstances",
        "eks:ListClusters",
        "eks:DescribeCluster",
        "elasticache:DescribeCacheClusters",
        "elasticache:DescribeReplicationGroups",
        "elasticache:DescribeServerlessCaches"
      ],
      "Resource": "*"
    },
    {
      "Sid": "PortForwardThroughTaggedJumphosts",
      "Effect": "Allow",
      "Action": "ssm:StartSession",
      "Resource": "arn:aws:ec2:<region>:<account-id>:instance/*",
      "Condition": { "StringEquals": { "ssm:resourceTag/Usage": "jumphost" } }
    },
    {
      "Sid": "PortForwardDocument",
      "Effect": "Allow",
      "Action": "ssm:StartSession",
      "Resource": "arn:aws:ssm:<region>::document/AWS-StartPortForwardingSessionToRemoteHost"
    },
    {
      "Sid": "ManageOwnSessions",
      "Effect": "Allow",
      "Action": ["ssm:TerminateSession", "ssm:ResumeSession"],
      "Resource": "arn:aws:ssm:*:*:session/${aws:userid}-*"
    },
    {
      "Sid": "SignInToDatabasesWithIAM",
      "Effect": "Allow",
      "Action": "rds-db:connect",
      "Resource": "arn:aws:rds-db:<region>:<account-id>:dbuser:<db-resource-id>/<db-user>"
    },
    {
      "Sid": "McpServerSecrets",
      "Effect": "Allow",
      "Action": ["secretsmanager:ListSecrets", "secretsmanager:GetSecretValue"],
      "Resource": "*"
    }
  ]
}
```

- `PortForwardThroughTaggedJumphosts` only allows sessions on instances carrying the jumphost tag
  (adapt the condition key if you changed `aws.jumphostTag`), and `PortForwardDocument` only allows
  port forwarding, not shell sessions.
- `McpServerSecrets` is only needed for SQL Server through the MCP server (read-only secrets, or the
  RDS-managed master user secret when writes are enabled); restrict `GetSecretValue` to those
  secrets' ARNs when you can.
- `airlock rds-token` signs the token locally: it needs no permission of its own beyond
  `rds-db:connect` on the database user.

## 6. Check

```bash
airlock config show          # aws.jumphostTag matches the tag of step 3
airlock rds <db-instance-id> # or just `airlock` for the wizard
```

If airlock reports that no running jumphost instance carries the tag, check the tag, that the
instance is running in the selected profile's account and region, and the `ec2:DescribeInstances`
permission.

## Optional hardening

- Session Manager preferences can encrypt sessions with a KMS key and log them to CloudWatch Logs or
  S3.
- One jumphost per VPC keeps security group rules simple; run it in an Auto Scaling group of one
  to have it replaced automatically if it fails.
