package context

import (
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/cli/cli/api"
	"github.com/cli/cli/git"
	"github.com/cli/cli/internal/ghrepo"
	"github.com/mitchellh/go-homedir"
)

// Context represents the interface for querying information about the current environment
type Context interface {
	AuthToken() (string, error)
	SetAuthToken(string)
	AuthLogin() (string, error)
	Branch() (string, error)
	SetBranch(string)
	Remotes() (Remotes, error)
	BaseRepo() (ghrepo.Interface, error)
	SetBaseRepo(string)
}

// cap the number of git remotes looked up, since the user might have an
// unusally large number of git remotes
const maxRemotesForLookup = 5

func ResolveRemotesToRepos(remotes Remotes, client *api.Client, base string) (ResolvedRemotes, error) {
	sort.Stable(remotes)
	lenRemotesForLookup := len(remotes)
	if lenRemotesForLookup > maxRemotesForLookup {
		lenRemotesForLookup = maxRemotesForLookup
	}

	hasBaseOverride := base != ""
	baseOverride := ghrepo.FromFullName(base)
	foundBaseOverride := false
	repos := []ghrepo.Interface{}
	for _, r := range remotes[:lenRemotesForLookup] {
		repos = append(repos, r)
		if ghrepo.IsSame(r, baseOverride) {
			foundBaseOverride = true
		}
	}
	if hasBaseOverride && !foundBaseOverride {
		// additionally, look up the explicitly specified base repo if it's not
		// already covered by git remotes
		repos = append(repos, baseOverride)
	}

	result := ResolvedRemotes{remotes: remotes}
	if hasBaseOverride {
		result.baseOverride = baseOverride
	}
	networkResult, err := api.RepoNetwork(client, repos)
	if err != nil {
		return result, err
	}
	result.network = networkResult
	return result, nil
}

type ResolvedRemotes struct {
	baseOverride ghrepo.Interface
	remotes      Remotes
	network      api.RepoNetworkResult
}

// BaseRepo is the first found repository in the "upstream", "github", "origin"
// git remote order, resolved to the parent repo if the git remote points to a fork
func (r ResolvedRemotes) BaseRepo() (*api.Repository, error) {
	if r.baseOverride != nil {
		for _, repo := range r.network.Repositories {
			if repo != nil && ghrepo.IsSame(repo, r.baseOverride) {
				return repo, nil
			}
		}
		return nil, fmt.Errorf("failed looking up information about the '%s' repository",
			ghrepo.FullName(r.baseOverride))
	}

	for _, repo := range r.network.Repositories {
		if repo == nil {
			continue
		}
		if repo.IsFork() {
			return repo.Parent, nil
		}
		return repo, nil
	}

	return nil, errors.New("not found")
}

// HeadRepo is the first found repository that has push access
func (r ResolvedRemotes) HeadRepo() (*api.Repository, error) {
	for _, repo := range r.network.Repositories {
		if repo != nil && repo.ViewerCanPush() {
			return repo, nil
		}
	}
	return nil, errors.New("none of the repositories have push access")
}

// RemoteForRepo finds the git remote that points to a repository
func (r ResolvedRemotes) RemoteForRepo(repo ghrepo.Interface) (*Remote, error) {
	for i, remote := range r.remotes {
		if ghrepo.IsSame(remote, repo) ||
			// additionally, look up the resolved repository name in case this
			// git remote points to this repository via a redirect
			(r.network.Repositories[i] != nil && ghrepo.IsSame(r.network.Repositories[i], repo)) {
			return remote, nil
		}
	}
	return nil, errors.New("not found")
}

// New initializes a Context that reads from the filesystem
func New() Context {
	return &fsContext{}
}

// A Context implementation that queries the filesystem
type fsContext struct {
	config    *configEntry
	remotes   Remotes
	branch    string
	baseRepo  ghrepo.Interface
	authToken string
}

func ConfigDir() string {
	dir, _ := homedir.Expand("~/.config/gh")
	return dir
}

func configFile() string {
	return path.Join(ConfigDir(), "config.yml")
}

func (c *fsContext) getConfig() (*configEntry, error) {
	if c.config == nil {
		entry, err := parseOrSetupConfigFile(configFile())
		if err != nil {
			return nil, err
		}
		c.config = entry
		c.authToken = ""
	}
	return c.config, nil
}

func (c *fsContext) AuthToken() (string, error) {
	if c.authToken != "" {
		return c.authToken, nil
	}

	config, err := c.getConfig()
	if err != nil {
		return "", err
	}
	return config.Token, nil
}

func (c *fsContext) SetAuthToken(t string) {
	c.authToken = t
}

func (c *fsContext) AuthLogin() (string, error) {
	config, err := c.getConfig()
	if err != nil {
		return "", err
	}
	return config.User, nil
}

func (c *fsContext) Branch() (string, error) {
	if c.branch != "" {
		return c.branch, nil
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		return "", err
	}

	c.branch = currentBranch
	return c.branch, nil
}

func (c *fsContext) SetBranch(b string) {
	c.branch = b
}

func (c *fsContext) Remotes() (Remotes, error) {
	if c.remotes == nil {
		gitRemotes, err := git.Remotes()
		if err != nil {
			return nil, err
		}
		sshTranslate := git.ParseSSHConfig().Translator()
		c.remotes = translateRemotes(gitRemotes, sshTranslate)
	}
	return c.remotes, nil
}

func (c *fsContext) BaseRepo() (ghrepo.Interface, error) {
	if c.baseRepo != nil {
		return c.baseRepo, nil
	}

	remotes, err := c.Remotes()
	if err != nil {
		return nil, err
	}
	rem, err := remotes.FindByName("upstream", "github", "origin", "*")
	if err != nil {
		return nil, err
	}

	c.baseRepo = rem
	return c.baseRepo, nil
}

func (c *fsContext) SetBaseRepo(nwo string) {
	c.baseRepo = ghrepo.FromFullName(nwo)
}