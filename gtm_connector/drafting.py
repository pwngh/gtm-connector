"""Draft results and the default fixture, which runs no model or tools."""

from dataclasses import dataclass, field

from .citations import build_facts


@dataclass(frozen=True)
class DraftResult:
    """Untrusted candidate text plus reported usage, never a publication result."""

    text: str | None
    model: str | None = None
    input_tokens: int | None = None
    output_tokens: int | None = None
    error_code: str | None = None
    metadata: dict[str, str] = field(default_factory=dict)


class FixtureDraftAdapter:
    """Format source facts for the demo; the workflow must still check the output.

    This is an extractive fixture, not an LLM or an autonomous tool-using agent.
    It accepts evidence only: no client, token, owner credential, or write tool.
    """

    name = "fixture"

    def draft(self, evidence: dict) -> str:
        return "\n".join(build_facts(evidence)) + "\n"
