
## Conversation Condensation Directive

The objective of this task is to generate an exhaustive summary of the dialogue to preserve critical context. The output must retain a high level of technical detail, architectural choices, and specific programming patterns so that development can resume seamlessly.

Prior to formatting the final summary, you must document your review process inside `<analysis>` tags to verify all details are captured. During this evaluation phase:

1. Review the dialogue sequentially from beginning to end. For every segment, meticulously document:
* The user’s precise goals and stated objectives.
* The strategy and methodology applied to fulfill those goals.
* Crucial architectural decisions, technical frameworks, and coding paradigms.
* Exact technical data, including file paths, complete code blocks, method signatures, and specific lines modified.


2. Verify the absolute technical correctness and thoroughness of your review before proceeding.

---

### Required Summary Framework

Your final output must be organized into the following distinct sections:

1. **Core Objectives and Intent:** A detailed breakdown of everything the user explicitly asked to achieve.
2. **Critical Technical Paradigms:** A structured list of the key methodologies, frameworks, and technical concepts utilized.
3. **Target Files and Code Repository:** A comprehensive inventory of the specific files evaluated, written, or edited. Highlight the most recent updates, provide full code blocks where relevant, and explain the significance of each file's involvement.
4. **Resolved and Active Issues:** A log of the technical hurdles overcome during the session, along with any active troubleshooting.
5. **Backlog of Requested Tasks:** A clear list of uncompleted items that the user has explicitly requested.
6. **Active State Prior to Summary:** A granular description of the exact task being executed immediately before this summary was triggered, with a heavy emphasis on the final exchange. Include relevant filenames and code fragments.
7. **Immediate Next Action (Conditional):** Define the single next logical step that directly extends the active state described above. This action **must** align perfectly with the user's explicit goals and immediate prior task. If the previous task was successfully finished, do not suggest tangential steps unless previously authorized by the user.
8. **Verbatim Contextual Evidence:** If a next step is defined, provide exact, unedited textual quotes from the final moments of the conversation to prove precisely where the work left off and prevent scope drift.

---

### Expected Output Structure

Your final response must strictly adhere to this format:

```markdown
<analysis>
[Insert your chronological reasoning, technical validation, and verification process here]
</analysis>

<summary>
1. Core Objectives and Intent:
   [Detailed breakdown]

2. Critical Technical Paradigms:
   - [Concept/Technology 1]
   - [Concept/Technology 2]

3. Target Files and Code Repository:
   - [File Path 1]
      - [Context and importance of this file]
      - [Details of modifications made]
      - [Relevant Code Block]
   - [File Path 2]
      - [Relevant Code Block]

4. Resolved and Active Issues:
   [Detailed log of troubleshooting and fixes]

5. Backlog of Requested Tasks:
   - [Task 1]
   - [Task 2]

6. Active State Prior to Summary:
   [Granular description of the latest interaction and current work state]

7. Immediate Next Action (Conditional):
   [Specific next step aligned with the user's intent]
</summary>

```

Please generate the summary now by applying this structural blueprint to the preceding conversation, ensuring total accuracy and depth.
