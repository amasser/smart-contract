# Deploy

IaaS, PaaS, system and container orchestration deployment configurations and templates (docker-compose, kubernetes/helm, mesos, terraform, bosh). The AWS deployment uses the [AWS Cloud Development Kit](https://github.com/awslabs/aws-cdk).

- [Docker](#deploy-docker)
- [AWS - ECS](#deploy-aws-ecs)
- [AWS - EC2](#deploy-aws-ec2)

<a name="deploy-docker"></a>
## Docker

Enter the `docker/` directory for performing these commands.

### Usage

Note: Ensure your configuration is correctly setup to use S3 for storage or you could lose data:

    docker pull tokenized/smartcontractd
    docker run --env-file ./smartcontractd.conf tokenized/smartcontractd

### Building

Build:

    docker build -t tokenized/smartcontractd -f ./Dockerfile ../../../

Run as a local test:

    docker run --rm -it --env-file ./smartcontractd.conf tokenized/smartcontractd

Push to dockerhub:

    docker login

    docker push tokenized/smartcontractd

<a name="deploy-aws-ecs"></a>
## AWS - ECS

Enter the `aws-ecs/` directory for performing these commands.

Refer to the [AWS EC2 deployment](#deploy-aws-ec2) for more detailed instructions, the process is mostly the same.

### Useful CDK Commands

 * `npm run build`   compile typescript to js
 * `npm run watch`   watch for changes and compile
 * `cdk deploy`      deploy this stack to your default AWS account/region
 * `cdk diff`        compare deployed stack with current state
 * `cdk synth`       emits the synthesized CloudFormation template

<a name="deploy-aws-ec2"></a>
## AWS - EC2

Enter the `aws-ec2/` directory for performing these commands.

Ensure you have an [AWS account](https://portal.aws.amazon.com/billing/signup#/start), you have the [AWS CLI installed](https://docs.aws.amazon.com/cli/latest/userguide/installing.html), and that your AWS CLI [credentials are correctly configured](https://docs.aws.amazon.com/cli/latest/userguide/cli-chap-getting-started.html):

    aws configure

Install [AWS CDK](https://github.com/awslabs/aws-cdk)

    npm i -g aws-cdk

Create or import an SSH Key Pair to allow access to the running server:

* https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-key-pairs.html
* https://docs.aws.amazon.com/cli/latest/reference/ec2/create-key-pair.html
* https://docs.aws.amazon.com/cli/latest/reference/ec2/import-key-pair.html
* https://docs.aws.amazon.com/cli/latest/reference/ec2/describe-key-pairs.html

Command:

    aws ec2 import-key-pair --key-name tokenized-ssh --public-key-material file://~/.ssh/tokenized-ssh.pub

Make any required configuration changes to `./bin/tokenized.ts` (eg. EC2 instance type/size, key name, etc). In particular, make sure you have updated the `TokenizedStack` properties to define appropriate values in `appConfig`:

    new TokenizedEC2Stack(app, 'TokenizedEC2Stack', {
        appConfig: {
            OPERATOR_NAME: "Standalone",
            VERSION: "0.1",
            FEE_ADDRESS: "yourfeeaddress",
            FEE_VALUE: 2000,
            NODE_ADDRESS: "1.2.3.4:8333",
            NODE_USER_AGENT: "",
            RPC_HOST: "1.2.3.4:8332",
            RPC_USERNAME: "youruser",
            RPC_PASSWORD: "yoursecretpassword",
            PRIV_KEY: "yourwif",
        },
        ec2InstanceClass: ec2.InstanceClass.T3,
        ec2InstanceSize: ec2.InstanceSize.Micro,
        enableSSH: false,
    });

Don't forget to compile any changes you made:

    npm run-script build
    # Or in another terminal, watch and recompile with: npm run-script watch

Make sure you have already built the `smatcontractd` binary:

    cd ../../ && make clean prepare deps dist-smartcontractd
    # Make sure to return to the deploy directory afterwards: cd ./deploy/aws-ec2

Check the differences between the currently deployed stack (if it exists), and the stack as defined in `./bin/tokenized.ts`: 

    cdk diff

If you're happy with the proposed changes, deploy the stack:

    cdk deploy

### SSH

You can SSH through the NAT instance using a `~/.ssh/config` setup similar to the following:

    Host tokenized-nat tokenised-nat
    Hostname 1.2.3.4
    User ec2-user
    IdentityFile ~/.ssh/tokenized-ssh
    IdentitiesOnly yes

    Host tokenized tokenised
    Hostname 10.0.111.195
    User ec2-user
    IdentityFile ~/.ssh/tokenized-ssh
    IdentitiesOnly yes
    ProxyCommand ssh -A tokenized-nat -W %h:%p

Note: Make sure you set the public IP of your NAT gateway + private IP of your tokenized server correctly.

Then you can just connect with:

    ssh tokenized

### Config

`config.template` uses [mustache](https://mustache.github.io/) template syntax to [generate the config file at deploy time](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-init.html#aws-resource-init-files).

### Service

`smartcontract.service.template` is configured for [systemd](https://www.freedesktop.org/software/systemd/man/systemd.service.html) (as [supported on Amazon Linux 2](https://aws.amazon.com/amazon-linux-2/release-notes/#systemd)), and [managed during deploy time](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-init.html#aws-resource-init-services).

### Useful CDK Commands

 * `npm run build`   compile typescript to js
 * `npm run watch`   watch for changes and compile
 * `cdk deploy`      deploy this stack to your default AWS account/region
 * `cdk diff`        compare deployed stack with current state
 * `cdk synth`       emits the synthesized CloudFormation template
