# Project Memory

## UI: Progressive Disclosure

- Default to a concise interface. Show only the information and controls needed for the user's current task and state.
- Do not add explanatory copy when headings, labels, choices, or surrounding controls already make the meaning clear.
- Do not expose implementation details in customer-facing copy. Keep diagnostics and advanced controls hidden until the user explicitly needs them.
- Reveal contextual information and conditional controls only when they become relevant.
- Preserve information that enables action or prevents harm: validation and recovery guidance, actionable empty states, non-obvious constraints, dynamic status, and the consequences of destructive operations.
- When removing visible descriptions, preserve accessible names and avoid dangling `aria-describedby` references.
