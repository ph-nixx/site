---
feature_name: (fill me in with a unique ident, `my_awesome_feature`)
start_date: (fill me in with today's date, YYYY-MM-DD)
status: open | implemented
---

## Summary
[summary]: #summary

One paragraph explanation of the feature.

## Non-goals
[non-goals]: #non-goals

Non-goals aren’t negated goals like “The system shouldn’t crash”, but rather things that could reasonably be goals, but are explicitly chosen not to be goals. A good example would be “ACID compliance”; when designing a database, you’d certainly want to know whether that is a goal or non-goal. And if it is a non-goal you might still select a solution that provides it, if it doesn’t introduce trade-offs that prevent achieving the goals.

## Motivation
[motivation]: #motivation

This section should explain a problem in detail, including necessary background. It should also contain several specific use cases where this feature can help a user, and explain how it helps. This can then be used to guide the design of the feature. This section is one of the most important sections of any RFC, and can be lengthy.

## Guide-level explanation
[guide-level-explanation]: #guide-level-explanation

Explain the feature as if it was already existed and you were teaching a friend how to use it. That generally means:

* Introducing new named concepts.
* Explaining the feature largely in terms of examples.
* Explaining how your friend should *think* about the feature. It should explain the impact as concretely as possible.

## Reference-level explanation
[reference-level-explanation]: #reference-level-explanation

This is the technical portion of the RFC. Explain the design in sufficient detail that:

* Its interaction with other features is clear.
* It is reasonably clear how the feature would be implemented.
* Corner cases are dissected by example.

The section should return to the examples given in the previous section, and explain more fully how the detailed proposal makes those examples work.

## Drawbacks
[drawbacks]: #drawbacks

Why should we *not* do this?

## Rationale and alternatives
[rationale-and-alternatives]: #rationale-and-alternatives

* Why is this design the best in the space of possible designs?
* What other designs have been considered and what is the rationale for not choosing them?
* What is the impact of not doing this?

## Prior art
[prior-art]: #prior-art

Discuss prior art, both the good and the bad, in relation to this proposal.
An examples of what this can include is: does this feature exist in other projects and what experience have their community had?

This section is intended to encourage you as an author to think about the lessons from other projects, provide readers of your RFC with a fuller picture. If there is no prior art, that is fine - your ideas are interesting to us whether they are brand new or if it is an adaptation from other languages. Note that while precedent set by other projects is some motivation, it does not on its own motivate an RFC.

## Unresolved questions
[unresolved-questions]: #unresolved-questions

* What parts of the design do you expect to resolve through the RFC process before this gets merged?
* What parts of the design do you expect to resolve through the implementation of this feature before stabilization?
* What related issues do you consider out of scope for this RFC that could be addressed in the future independently of the solution that comes out of this RFC?

## Future possibilities
[future-possibilities]: #future-possibilities

Think about what the natural extension and evolution of your proposal would be and how it would affect the language and project as a whole in a holistic way. Try to use this section as a tool to more fully consider all possible interactions with the project and language in your proposal. Also consider how this all fits into the roadmap for the project and of the implementers.
This is also a good place to "dump ideas", if they are out of scope for the RFC you are writing but otherwise related. If you have tried and cannot think of any future possibilities, you may simply state that you cannot think of anything. Note that having something written down in the future-possibilities section is not a reason to accept the current or a future RFC; such notes should be in the section on motivation or rationale in this or subsequent RFCs. The section merely provides additional information.
