
[Automated context-compression directive — triggered by the user, injected by the application.]

Generate an exhaustive summary of the preceding conversation to preserve critical context for continuation. The output must retain high technical detail: architectural choices, specific programming patterns, file paths, method signatures, and code fragments.

Before writing the final summary, document your review inside `<analysis>` tags to verify all details are captured:

1. Review the dialogue sequentially from beginning to end. For every segment document:
   - The user's precise goals and stated objectives
   - The strategy and methodology applied
   - Architectural decisions, technical frameworks, and coding paradigms
   - Exact technical data: file paths, complete code blocks, method signatures, specific lines changed

2. Verify technical correctness and completeness before proceeding.

---

### Required Summary Structure

Organize the output into these sections:

1. **Core Objectives and Intent** — Everything the user explicitly asked to achieve.
2. **Critical Technical Paradigms** — Key methodologies, frameworks, and concepts used.
3. **Target Files and Code Repository** — All files touched: context, modifications, and relevant code blocks.
4. **Resolved and Active Issues** — Technical problems overcome and any still open.
5. **Backlog of Requested Tasks** — Uncompleted items explicitly requested by the user.
6. **Active State Prior to Summary** — The exact task executing when this directive was triggered, with emphasis on the final exchange.
7. **Immediate Next Action (Conditional)** — The single next logical step extending the active state. Must align with the user's explicit goals. Omit if the previous task was cleanly finished with no authorized follow-on.
8. **Verbatim Contextual Evidence** — If a next step is defined, exact unedited quotes from the final exchange proving where work left off.

---

### Output Format

```markdown
<analysis>
[Chronological reasoning, technical validation, and verification]
</analysis>

<summary>
1. Core Objectives and Intent:
   [Detailed breakdown]

2. Critical Technical Paradigms:
   - [Concept/Technology 1]
   - [Concept/Technology 2]

3. Target Files and Code Repository:
   - [File Path 1]
      - [Context and importance]
      - [Modifications made]
      - [Relevant code block]
   - [File Path 2]
      - [Relevant code block]

4. Resolved and Active Issues:
   [Detailed log]

5. Backlog of Requested Tasks:
   - [Task 1]
   - [Task 2]

6. Active State Prior to Summary:
   [Granular description of the latest interaction and current work state]

7. Immediate Next Action (Conditional):
   [Specific next step]

8. Verbatim Contextual Evidence:
   [Exact quotes from the final exchange]
</summary>
```

Execute this directive now.
