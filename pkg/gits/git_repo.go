package gits

import (
	"fmt"
	"io"
	"strings"

	"github.com/jenkins-x/jx/pkg/auth"
	"github.com/jenkins-x/jx/pkg/util"
	survey "gopkg.in/AlecAivazis/survey.v1"
	"gopkg.in/AlecAivazis/survey.v1/terminal"
)

type CreateRepoData struct {
	Organisation string
	RepoName     string
	FullName     string
	PrivateRepo  bool
	User         *auth.UserAuth
	Server       *auth.AuthServer
	GitProvider  GitProvider
}

type GitRepositoryOptions struct {
	ServerURL  string
	ServerKind string
	Username   string
	ApiToken   string
	Owner      string
	RepoName   string
	Private    bool
}

// GetRepository returns the repository if it already exists
func (d *CreateRepoData) GetRepository() (*GitRepository, error) {
	return d.GitProvider.GetRepository(d.Organisation, d.RepoName)
}

// CreateRepository creates the repository - failing if it already exists
func (d *CreateRepoData) CreateRepository() (*GitRepository, error) {
	return d.GitProvider.CreateRepository(d.Organisation, d.RepoName, d.PrivateRepo)
}

func PickNewOrExistingGitRepository(batchMode bool, authConfigSvc auth.ConfigService, defaultRepoName string,
	repoOptions *GitRepositoryOptions, server *auth.AuthServer, userAuth *auth.UserAuth, git Gitter, allowExistingRepo bool, in terminal.FileReader, out terminal.FileWriter, errOut io.Writer) (*CreateRepoData, error) {
	config := authConfigSvc.Config()

	var err error
	if server == nil {
		if repoOptions.ServerURL != "" {
			server = config.GetOrCreateServer(repoOptions.ServerURL)
		} else {
			if batchMode {
				if len(config.Servers) == 0 {
					return nil, fmt.Errorf("No Git servers are configured!")
				}
				// lets assume the first for now
				server = config.Servers[0]
				currentServer := config.CurrentServer
				if currentServer != "" {
					for _, s := range config.Servers {
						if s.Name == currentServer || s.URL == currentServer {
							server = s
							break
						}
					}
				}
			} else {
				server, err = config.PickServer("Which Git service?", batchMode, in, out, errOut)
				if err != nil {
					return nil, err
				}
			}
			repoOptions.ServerURL = server.URL
		}
	}
	fmt.Fprintf(out, "Using Git provider %s\n", util.ColorInfo(server.Description()))
	url := server.URL

	if userAuth == nil {
		if repoOptions.Username != "" {
			userAuth = config.GetOrCreateUserAuth(url, repoOptions.Username)
		} else {
			if batchMode {
				if len(server.Users) == 0 {
					return nil, fmt.Errorf("Server %s has no user auths defined", url)
				}
				var ua *auth.UserAuth
				if server.CurrentUser != "" {
					ua = config.FindUserAuth(url, server.CurrentUser)
				}
				if ua == nil {
					ua = server.Users[0]
				}
				userAuth = ua
			} else {
				userAuth, err = config.PickServerUserAuth(server, "Git user name?", batchMode, "", in, out, errOut)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	if userAuth.IsInvalid() && repoOptions.ApiToken != "" {
		userAuth.ApiToken = repoOptions.ApiToken
	}

	if userAuth.IsInvalid() {
		f := func(username string) error {
			git.PrintCreateRepositoryGenerateAccessToken(server, username, out)
			return nil
		}

		// TODO could we guess this based on the users ~/.git for github?
		defaultUserName := ""
		err = config.EditUserAuth(server.Label(), userAuth, defaultUserName, true, batchMode, f, in, out, errOut)
		if err != nil {
			return nil, err
		}

		// TODO lets verify the auth works

		err = authConfigSvc.SaveUserAuth(url, userAuth)
		if err != nil {
			return nil, fmt.Errorf("Failed to store git auth configuration %s", err)
		}
		if userAuth.IsInvalid() {
			return nil, fmt.Errorf("You did not properly define the user authentication")
		}
	}

	gitUsername := userAuth.Username
	fmt.Fprintf(out, "\n\nAbout to create repository %s on server %s with user %s\n", util.ColorInfo(defaultRepoName), util.ColorInfo(url), util.ColorInfo(gitUsername))

	provider, err := CreateProvider(server, userAuth, git)
	if err != nil {
		return nil, err
	}
	owner := repoOptions.Owner
	if owner == "" {
		owner, err = GetOwner(batchMode, provider, gitUsername, in, out, errOut)
		if err != nil {
			return nil, err
		}
	}
	repoName := repoOptions.RepoName
	if repoName == "" {
		repoName, err = GetRepoName(batchMode, allowExistingRepo, provider, defaultRepoName, owner, in, out, errOut)
		if err != nil {
			return nil, err
		}
	}

	fullName := git.RepoName(owner, repoName)
	fmt.Fprintf(out, "\n\nCreating repository %s\n", util.ColorInfo(fullName))

	return &CreateRepoData{
		Organisation: owner,
		RepoName:     repoName,
		FullName:     fullName,
		PrivateRepo:  repoOptions.Private,
		User:         userAuth,
		GitProvider:  provider,
		Server:       server,
	}, err
}

func GetRepoName(batchMode, allowExistingRepo bool, provider GitProvider, defaultRepoName, owner string, in terminal.FileReader, out terminal.FileWriter, errOut io.Writer) (string, error) {
	surveyOpts := survey.WithStdio(in, out, errOut)
	repoName := ""
	if batchMode {
		repoName = defaultRepoName
		if repoName == "" {
			repoName = "dummy"
		}
	} else {
		prompt := &survey.Input{
			Message: "Enter the new repository name: ",
			Default: defaultRepoName,
		}
		validator := func(val interface{}) error {
			str, ok := val.(string)
			if !ok {
				return fmt.Errorf("Expected string value")
			}
			if strings.TrimSpace(str) == "" {
				return fmt.Errorf("Repository name is required")
			}
			if allowExistingRepo {
				return nil
			}
			return provider.ValidateRepositoryName(owner, str)
		}
		err := survey.AskOne(prompt, &repoName, validator, surveyOpts)
		if err != nil {
			return "", err
		}
		if repoName == "" {
			return "", fmt.Errorf("No repository name specified")
		}
	}
	return repoName, nil
}

func GetOwner(batchMode bool, provider GitProvider, gitUsername string, in terminal.FileReader, out terminal.FileWriter, errOut io.Writer) (string, error) {
	owner := ""
	if batchMode {
		owner = gitUsername
	} else {
		org, err := PickOrganisation(provider, gitUsername, in, out, errOut)
		if err != nil {
			return "", err
		}
		owner = org
		if org == "" {
			owner = gitUsername
		}
	}
	return owner, nil
}

func PickNewGitRepository(batchMode bool, authConfigSvc auth.ConfigService, defaultRepoName string,
	repoOptions *GitRepositoryOptions, server *auth.AuthServer, userAuth *auth.UserAuth, git Gitter, in terminal.FileReader, out terminal.FileWriter, outErr io.Writer) (*CreateRepoData, error) {
	return PickNewOrExistingGitRepository(batchMode, authConfigSvc, defaultRepoName, repoOptions, server, userAuth, git, false, in, out, outErr)
}
