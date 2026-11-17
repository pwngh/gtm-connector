"""A deterministic draft fixture, with no model or tool execution."""

from .citations import build_facts


class FixtureDraftAdapter:
    """Format source facts for the demo; the workflow must still check the output.

    This is an extractive fixture, not an LLM or an autonomous tool-using agent.
    It accepts evidence only: no client, token, owner credential, or write tool.
    """

    name = "fixture"

    def draft(self, evidence: dict) -> str:
        return "\n".join(build_facts(evidence)) + "\n"
