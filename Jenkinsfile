pipeline {
  agent {
    label 'crew-platform' // Each crew has an agent/label available (crew-2, crew-apollo, crew-appliance, crew-auth, crew-brokkr, crew-brucke, crew-guardian, crew-services, crew-skynet, crew-mktg, cs-infra)
  }

  environment { // This block defines environment variables that will be available throughout the rest of the pipeline
    SERVICE_NAME = 'faros'
  }

  options {
    timeout(time: 10, unit: 'MINUTES') // Global timeout for the job. Recommended to make the job fail if it's taking too long
  }

  parameters { // Job parameters that need to be supplied when the job is run. If they have a default value they won't be required
    string(name: 'SlackTarget', defaultValue: '#platform-deployments', description: 'Target Slack Channel for notifications')
  }

  stages {
    stage('SharedLibs') { // Required. Stage to load the Auth0 shared library for Jenkinsfile
      steps {
        library identifier: 'auth0-jenkins-pipelines-library@master', retriever: modernSCM(
          [$class: 'GitSCMSource',
          remote: 'git@github.com:auth0/auth0-jenkins-pipelines-library.git',
          credentialsId: 'auth0extensions-ssh-key'])
      }
    }

    stage('Unit Test') {
      steps {
        script {
          try {
            echo "Unit Testing `${env.SERVICE_NAME}`"
            sh """touch .env
            make docker-test"""
            githubNotify context: 'jenkinsfile/auth0/acceptance-test', description: 'Tests passed', status: 'SUCCESS'
          } catch (error) {
            githubNotify context: 'jenkinsfile/auth0/acceptance-test', description: 'Tests failed', status: 'FAILURE'
            throw error
          }
        }
        // Find more examples of what to add here at https://github.com/auth0/auth0-users/blob/master/Jenkinsfile#L70
      }
    }

    stage('Build Docker Image') {
      steps {
        script {
          DOCKER_REGISTRY = getDockerRegistry()
          DOCKER_TAG = getDockerTag()
          DOCKER_IMAGE_NAME = "${DOCKER_REGISTRY}/${env.SERVICE_NAME}:${DOCKER_TAG}"
          DOCKER_IMAGE_LATEST = "${DOCKER_REGISTRY}/${env.SERVICE_NAME}:latest"
          currentBuild.description = "${DOCKER_TAG}"

          withDockerRegistry(getArtifactoryRegistry()) {
            withCredentials([[$class: 'StringBinding', credentialsId: 'auth0extensions-token', variable: 'GITHUB_TOKEN']]) {
              sh """
                docker build \
                  --tag ${DOCKER_IMAGE_NAME} \
                  --build-arg GITHUB_TOKEN=${GITHUB_TOKEN} \
                  --build-arg VERSION=${DOCKER_TAG} \
                  .
              """
            }
          }
        }
      }
    }

    stage('Push Docker Image') {
      steps {
        dockerPushArtifactory(DOCKER_IMAGE_NAME)
      }
    }

    stage('Update "latest" Tag') {
      when {
        branch 'master'
      }
      steps {
        script {
          withDockerRegistry(getArtifactoryRegistry()) {
            sh """
              docker tag ${DOCKER_IMAGE_NAME} ${DOCKER_IMAGE_LATEST}
              docker push ${DOCKER_IMAGE_LATEST}
            """
          }
        }
      }
    }
  }

  post {
    always { // Steps that need to run regardless of the job status, such as test results publishing, Slack notifications or dependencies cleanup
      // Publish test results
      // junit allowEmptyResults: true, testResults: 'junit.xml' // Requires 'JUnit' Jenkins plugin installed

      script {
        String additionalMessage = ''
        String buildResult = currentBuild.result ?: 'SUCCESS'
        if (buildResult == 'SUCCESS') {
          additionalMessage += "\n```pushed: ${DOCKER_IMAGE_NAME}```"
        }
        notifySlack(params.SlackTarget, additionalMessage)
      }

      // Find more examples of what to add here at https://github.com/auth0/auth0-users/blob/master/Jenkinsfile#L191
    }
    cleanup {
      // Recommended to clean the workspace after every run
      deleteDir()
      dockerRemoveImage(DOCKER_IMAGE_NAME)
      dockerRemoveImage(DOCKER_IMAGE_LATEST)
    }
  }
}
