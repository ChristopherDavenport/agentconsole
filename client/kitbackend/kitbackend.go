// Package kitbackend is the native backend over an [agentkit.Kit]: the
// agent a product assembled with agentkit, driven in process by the
// terminal client.
//
// It is its own package, and not a constructor in package native, for the
// weight of the import. native needs the agent and the recorder and
// nothing else; agentkit imports every library of the stack, an MCP
// client among them. An embedder that builds its agentturn.Agent by hand
// keeps importing native alone.
//
// New builds the agent from the kit ([agentkit.Kit.Config] and
// [agentkit.Kit.AgentOptions]), attaches the kit's recorder to it
// ([agentkit.Kit.Attach]) and follows the store the kit's session is
// written to. Everything else is the kit's: the policy, the tool
// elicitor, the skill grants and the session. The embedder builds the kit
// with them, and for a session to resume it passes
// [agentkit.WithResumedSession], whose pending calls are restored on the
// agent, so a call held for approval before a restart is answered in the
// client after it.
//
//	kit, err := agentkit.New(ctx,
//		agentkit.WithModel(model, "gpt-5"),
//		agentkit.WithTools(read, write, bash),
//		agentkit.WithPolicy(policy, matchers),
//		agentkit.WithSession(store, header),
//	)
//	if err != nil {
//		return err
//	}
//	defer kit.Close()
//	be, err := kitbackend.New(kit)
//	if err != nil {
//		return err
//	}
//	defer be.Close()
//	return console.Run(ctx, be)
package kitbackend

import (
	"context"
	"errors"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentmemory"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/client/native"
)

// Backend is a [native.Backend] over a kit. Its Control, Live and Record
// are the native backend's, with what the kit expects of a front added to
// each run:
//
//   - every run (Prompt, Answer) and every head move carries the kit's
//     recorder ([agentkit.ContextWithRecorder]), its session ID
//     ([session.ContextWithSessionID], [agentmemory.WithSession]), and
//     with them the conversation's grant scope ([agentkit.Kit.GrantScope]):
//     the verdicts, the memory manifest, a fold, a tool's question and its
//     answer, and a skill's grants are recorded in, and decided for, the
//     session the client follows;
//   - Answer puts the answers through the kit's engine
//     ([agentpolicy.Engine.Release]) before it resumes the run, so a call
//     the engine only held for another call's ask is released with the
//     batch, and a call nobody answered is an error that names it, not a
//     failure of the loop;
//   - ContinueFrom ends the conversation's skill grants before the head
//     leaves a branch and grants again the ones the new branch's reads
//     made ([agentkit.Kit.RevokeSkillGrants], [agentkit.Kit.RegrantSkills]),
//     so a tool a skill on the old branch allowed is not allowed on the
//     new one, and the other way round.
//
// The questions a tool asks mid-call reach the elicitor the kit was built
// with ([agentkit.WithToolElicitor]); the terminal client has no screen
// for them yet, so the embedder's function answers them.
type Backend struct {
	*native.Backend
	kit    *agentkit.Kit
	detach func()
}

// Option configures [New].
type Option func(*options)

type options struct {
	config func(agentturn.Config) agentturn.Config
}

// WithConfig adjusts the agent's configuration after the kit built it,
// for what the client needs on top of the kit's (a hook of the
// embedder's, say). The function must keep the kit's fields: it receives
// [agentkit.Kit.Config] and returns what the agent is built from.
func WithConfig(fn func(agentturn.Config) agentturn.Config) Option {
	return func(o *options) { o.config = fn }
}

// New builds the agent from kit, attaches the kit to it and returns the
// backend. The kit must record a session ([agentkit.WithSession],
// [agentkit.WithResumedSession] or [agentkit.WithRecorder]): the client
// renders the record, and without one there is nothing to follow.
//
// Close detaches the recorder; closing the kit and the store stays the
// embedder's, after [console.Run] has returned.
//
// Under [agentkit.WithRecorder] the owner of the recorder attaches it, as
// the kit does not, and has to do so to this backend's agent before the
// first run: [Backend.Agent] returns it, and the recorder has to be
// subscribed ahead of the client for an event's entry to be in the store
// before the client sees the event.
func New(kit *agentkit.Kit, opts ...Option) (*Backend, error) {
	if kit == nil {
		return nil, errors.New("kitbackend: no kit")
	}
	rec := kit.Recorder()
	if rec == nil {
		return nil, errors.New("kitbackend: the kit records no session; build it with agentkit.WithSession, WithResumedSession or WithRecorder")
	}
	var o options
	for _, f := range opts {
		f(&o)
	}
	cfg := kit.Config()
	if o.config != nil {
		cfg = o.config(cfg)
	}
	agent := agentturn.New(cfg, kit.AgentOptions()...)
	// The kit's recorder subscribes first, so an entry is in the store
	// before the client sees the event that wrote it.
	detach := kit.Attach(agent)

	nopts := []native.Option{
		native.WithRunContext(func(ctx context.Context) context.Context { return runContext(ctx, rec) }),
		native.WithHeadMove(
			func(ctx context.Context) error {
				kit.RevokeSkillGrants(ctx)
				return nil
			},
			func(ctx context.Context, s *agentsession.Session) error { return kit.RegrantSkills(ctx, s) },
		),
	}
	if eng := kit.Engine(); eng != nil {
		nopts = append(nopts, native.WithRelease(func(ctx context.Context, end *agentturn.RunEnd, answers []agentturn.Answer) ([]agentturn.Answer, error) {
			return eng.Release(ctx, end, answers...)
		}))
	}
	nb, err := native.New(agent, rec, nopts...)
	if err != nil {
		detach()
		return nil, err
	}
	return &Backend{Backend: nb, kit: kit, detach: detach}, nil
}

// runContext puts on ctx what the kit expects of the front that prompts
// the conversation recorded by rec.
func runContext(ctx context.Context, rec *session.Recorder) context.Context {
	ctx = agentkit.ContextWithRecorder(ctx, rec)
	ctx = session.ContextWithSessionID(ctx, rec.SessionID())
	// Memory a run writes names the session; the kit cannot put the ID on
	// a context that is the host's.
	ctx = agentmemory.WithSession(ctx, rec.SessionID())
	return ctx
}

// Kit returns the kit the backend was built over.
func (b *Backend) Kit() *agentkit.Kit { return b.kit }

// Close detaches the kit's recorder from the agent. It does not close the
// kit or its store.
func (b *Backend) Close() error {
	b.detach()
	return nil
}
