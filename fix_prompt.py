import sys

path = 'internal/toolcall/toolprompt.go'
with open(path, 'r', encoding='utf-8') as f:
    content = f.read()

old = 'the command does.\\n\\nPARAMETER SHAPES:'
new = ('the command does.\\n'
       '20) CRITICAL - WHEN TO STOP CALLING TOOLS: After 1-3 tool calls, you MUST synthesize the results and provide a final text answer to the user. Do NOT keep calling tools repeatedly. If you searched and got results, ANSWER THE USER with what you found. If a search returned no useful results, say so and offer alternatives. NEVER enter an infinite loop of tool calls.\\n'
       "21) CRITICAL - TOOL CALLS ARE NOT YOUR FINAL ANSWER: A tool call is an intermediate step. After getting tool results, your NEXT response MUST be plain text answering the user's question. Do NOT respond to tool results with another tool call unless absolutely necessary (max 3 tool calls per question).\\n"
       '\\nPARAMETER SHAPES:')

if old in content:
    content = content.replace(old, new)
    with open(path, 'w', encoding='utf-8') as f:
        f.write(content)
    print('OK - patched')
else:
    print('Pattern not found')
    idx = content.find('command does')
    if idx >= 0:
        print(repr(content[idx:idx+100]))
    sys.exit(1)
