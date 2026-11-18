// Package codecommit implements the fixed-origin AWS CodeCommit JSON and
// regional STS protocols. Tenant authorization, persistence and retry scheduling
// belong to the caller. No default credential chain is consulted.
package codecommit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxGraphObjects   = 2000
	MaxPushCommits    = 100
	MaxReadBytes      = 8 << 20
	JobTimeout        = 20 * time.Second
	MaxPageTokenBytes = 8192
)

var (
	ErrInvalid             = errors.New("codecommit: invalid input or response")
	ErrIdentityMismatch    = errors.New("codecommit: account or repository identity mismatch")
	ErrCredentials         = errors.New("codecommit: credentials invalid or expired")
	ErrForbidden           = errors.New("codecommit: permission denied")
	ErrKMS                 = errors.New("codecommit: encryption key unavailable")
	ErrNotFound            = errors.New("codecommit: object not found")
	ErrRateLimited         = errors.New("codecommit: rate limited")
	ErrUnavailable         = errors.New("codecommit: request unavailable")
	ErrUnknownResult       = errors.New("codecommit: write result unknown")
	ErrIdempotencyConflict = errors.New("codecommit: idempotency parameters conflict")
	ErrIncomplete          = errors.New("codecommit: incomplete response or pagination")
	ErrGraphBudget         = errors.New("codecommit: commit_graph_budget_exceeded")
	ErrCommitLimit         = errors.New("codecommit: commit_limit")
	ErrGraphInvalid        = errors.New("codecommit: invalid commit graph")
)

// Error exposes only fixed classification; AWS messages, tokens and bodies are
// deliberately not retained. RetryAfter is a bounded scheduling hint, not retry.
type Error struct {
	Kind       error
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return "codecommit: " + e.Code }
func (e *Error) Unwrap() error { return e.Kind }

type Credentials struct {
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	SessionToken    string `json:"-"`
}

func (Credentials) String() string   { return "[AWS credentials redacted]" }
func (Credentials) GoString() string { return "[AWS credentials redacted]" }
func (c Credentials) Validate() error {
	if !keyPattern.MatchString(c.AccessKeyID) || !opaque(c.SecretAccessKey, 4096) || (c.SessionToken != "" && !opaque(c.SessionToken, 12288)) || len(c.AccessKeyID)+len(c.SecretAccessKey)+len(c.SessionToken) > 16384 {
		return ErrInvalid
	}
	return nil
}

var keyPattern = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func opaque(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func ValidAccount(s string) bool        { return accountPattern.MatchString(s) }
func ValidRepositoryName(s string) bool { return namePattern.MatchString(s) }
func ValidRepositoryID(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ValidSHA(s string) bool { return shaPattern.MatchString(s) && s != strings.Repeat("0", 40) }

// PullRequestNumber is an OpenFox int32 storage limit, not an AWS guarantee.
func PullRequestNumber(s string) (int32, error) {
	n, e := strconv.ParseInt(s, 10, 32)
	if e != nil || n < 1 || strconv.FormatInt(n, 10) != s {
		return 0, ErrInvalid
	}
	return int32(n), nil
}
func validCursor(s string) bool { return s == "" || opaque(s, MaxPageTokenBytes) }
func validBranch(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

type Identity struct {
	Account string `json:"account"`
	ARN     string `json:"arn"`
	UserID  string `json:"user_id"`
}
type RepositoryRef struct {
	ID   string `json:"repositoryId"`
	Name string `json:"repositoryName"`
}
type Repository struct {
	AccountID        string  `json:"accountId"`
	ARN              string  `json:"Arn"`
	ID               string  `json:"repositoryId"`
	Name             string  `json:"repositoryName"`
	DefaultBranch    string  `json:"defaultBranch"`
	CreationDate     float64 `json:"creationDate"`
	LastModifiedDate float64 `json:"lastModifiedDate"`
}
type RepositoryPage struct {
	Repositories []RepositoryRef `json:"repositories"`
	NextToken    string          `json:"nextToken"`
}
type PullRequestPage struct {
	IDs       []string `json:"pullRequestIds"`
	NextToken string   `json:"nextToken"`
}
type BranchPage struct {
	Branches  []string `json:"branches"`
	NextToken string   `json:"nextToken"`
}
type Branch struct {
	Name     string `json:"branchName"`
	CommitID string `json:"commitId"`
}
type PullRequestTarget struct {
	RepositoryName       string `json:"repositoryName"`
	SourceCommit         string `json:"sourceCommit"`
	SourceReference      string `json:"sourceReference"`
	DestinationCommit    string `json:"destinationCommit"`
	DestinationReference string `json:"destinationReference"`
	MergeMetadata        struct {
		IsMerged      bool   `json:"isMerged"`
		MergeCommitID string `json:"mergeCommitId"`
	} `json:"mergeMetadata"`
}
type PullRequest struct {
	ID               string              `json:"pullRequestId"`
	Title            string              `json:"title"`
	Description      string              `json:"description"`
	Status           string              `json:"pullRequestStatus"`
	AuthorARN        string              `json:"authorArn"`
	RevisionID       string              `json:"revisionId"`
	CreationDate     float64             `json:"creationDate"`
	LastActivityDate float64             `json:"lastActivityDate"`
	Targets          []PullRequestTarget `json:"pullRequestTargets"`
}
type Signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}
type Commit struct {
	ID        string    `json:"commitId"`
	TreeID    string    `json:"treeId"`
	Message   string    `json:"message"`
	Parents   []string  `json:"parents"`
	Author    Signature `json:"author"`
	Committer Signature `json:"committer"`
}
type CreatedEvidence struct {
	RepositoryName string
	PullRequestID  string
	BeforeCommitID string
	SourceCommitID string
	EventDate      float64
}

type commentRequest struct {
	RepositoryName     string `json:"repositoryName"`
	PullRequestID      string `json:"pullRequestId"`
	BeforeCommitID     string `json:"beforeCommitId"`
	AfterCommitID      string `json:"afterCommitId"`
	ClientRequestToken string `json:"clientRequestToken"`
	Content            string `json:"content"`
}

// CommentIntent is immutable after validation. Persist its JSON before sending;
// restoring the same JSON preserves token and every parameter. It has no Location.
type CommentIntent struct{ request commentRequest }

func NewCommentIntent(repo, id, before, after, token, body string) (CommentIntent, error) {
	r := commentRequest{repo, id, before, after, token, body}
	return validatedIntent(r)
}
func validatedIntent(r commentRequest) (CommentIntent, error) {
	_, err := PullRequestNumber(r.PullRequestID)
	if !ValidRepositoryName(r.RepositoryName) || err != nil || !ValidSHA(r.BeforeCommitID) || !ValidSHA(r.AfterCommitID) || r.BeforeCommitID == r.AfterCommitID || !opaque(r.ClientRequestToken, 128) || strings.TrimSpace(r.Content) == "" || !utf8.ValidString(r.Content) || utf8.RuneCountInString(r.Content) > 10240 || len(r.Content) > 32<<10 {
		return CommentIntent{}, ErrInvalid
	}
	return CommentIntent{r}, nil
}
func ParseCommentIntent(raw []byte) (CommentIntent, error) {
	if len(raw) > 64<<10 {
		return CommentIntent{}, ErrInvalid
	}
	var r commentRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil {
		return CommentIntent{}, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return CommentIntent{}, ErrInvalid
	}
	return validatedIntent(r)
}
func (i CommentIntent) MarshalJSON() ([]byte, error) {
	if _, err := validatedIntent(i.request); err != nil {
		return nil, err
	}
	return json.Marshal(i.request)
}
func (i CommentIntent) Fingerprint() string {
	raw, err := i.MarshalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (i CommentIntent) Token() string          { return i.request.ClientRequestToken }
func (i CommentIntent) RepositoryName() string { return i.request.RepositoryName }
func (i CommentIntent) PullRequestID() string  { return i.request.PullRequestID }

type CommentResult struct {
	ID           string
	AuthorARN    string
	CreationDate float64
}
