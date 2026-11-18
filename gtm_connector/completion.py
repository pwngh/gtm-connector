"""Generate shell completion from the public argparse command definitions."""

import argparse
import shlex


def _commands(parser):
    return next(action.choices for action in parser._actions if isinstance(action, argparse._SubParsersAction))


def _options(parser):
    return [action for action in parser._actions if action.option_strings]


def _words(words):
    return " ".join(shlex.quote(word) for word in words)


def generate(parser, shell):
    commands = _commands(parser)
    root = [*commands, *(flag for action in _options(parser) for flag in action.option_strings)]
    if shell == "fish":
        lines = ["# Load with: gtm-connector completion fish | source"]
        for command, subparser in commands.items():
            lines.append(f"complete -c gtm-connector -n '__fish_use_subcommand' -a {shlex.quote(command)} "
                         f"-d {shlex.quote(subparser.description)}")
        for name, subparser in [("", parser), *commands.items()]:
            condition = "__fish_use_subcommand" if not name else f"__fish_seen_subcommand_from {name}"
            for action in _options(subparser):
                flags = " ".join(("-l " + flag[2:]) if flag.startswith("--") else ("-s " + flag[1:])
                                 for flag in action.option_strings)
                line = f"complete -c gtm-connector -n {shlex.quote(condition)} {flags}"
                if action.nargs != 0:
                    line += " -r"
                if action.choices:
                    line += " -f -a " + shlex.quote(" ".join(map(str, action.choices)))
                lines.append(line + " -d " + shlex.quote(action.help or ""))
            for action in subparser._actions:
                if not action.option_strings and action.choices and not isinstance(action, argparse._SubParsersAction):
                    lines.append(f"complete -c gtm-connector -n {shlex.quote(condition)} -f -a "
                                 + shlex.quote(" ".join(map(str, action.choices))))
        return "\n".join(lines) + "\n"
    if shell not in ("bash", "zsh"):
        raise ValueError("Choose bash, zsh, or fish.")
    bash = shell == "bash"
    function = "_gtm_connector_complete" if bash else "_gtm_connector"
    lines = [f"{function}() {{"]
    if bash:
        lines += ['  local command="" word previous="${COMP_WORDS[COMP_CWORD-1]}"',
                  '  local current="${COMP_WORDS[COMP_CWORD]}" candidates="" match=""',
                  '  COMPREPLY=()', '  for word in "${COMP_WORDS[@]:1:COMP_CWORD-1}"; do']
    else:
        lines += ['  local command="" word previous="${words[CURRENT-1]}"',
                  '  local current="${words[CURRENT]}" index',
                  '  for ((index=2; index<CURRENT; index++)); do', '    word="${words[index]}"']
    lines += [f"    case \"$word\" in {'|'.join(commands)}) command=\"$word\"; break;; esac", '  done']

    def offer(values):
        if bash:
            return ["      candidates=" + shlex.quote(" ".join(values)),
                    '      while IFS= read -r match; do COMPREPLY+=("$match"); done '
                    '< <(compgen -W "$candidates" -- "$current"); return;;']
        return [f"      compadd -- {_words(values)}; return;;"]

    lines.append('  case "$command:$previous" in')
    for name, subparser in commands.items():
        for action in _options(subparser):
            if action.nargs == 0:
                continue
            patterns = "|".join(shlex.quote(name + ":" + flag) for flag in action.option_strings)
            lines.append(f"    {patterns})")
            if action.choices:
                lines += offer(list(map(str, action.choices)))
            elif action.type is not None and getattr(action.type, "__name__", "") == "Path":
                lines += (['      while IFS= read -r match; do COMPREPLY+=("$match"); done '
                           '< <(compgen -f -- "$current"); return;;'] if bash
                          else ['      _files; return;;'])
            else:
                lines.append('      return;;')
        positional = [action for action in subparser._actions if not action.option_strings and action.choices]
        if positional:
            lines.append(f"    {shlex.quote(name + ':' + name)})")
            lines += offer(list(map(str, positional[0].choices)))
    lines.append('  esac')
    lines.append('  case "$command" in')
    lines.append('    "")')
    lines += offer(root)
    for name, subparser in commands.items():
        lines.append(f"    {name})")
        lines += offer([flag for action in _options(subparser) for flag in action.option_strings])
    lines += ['  esac', '}']
    lines.append(f"complete -o default -F {function} gtm-connector" if bash else "compdef _gtm_connector gtm-connector")
    return "\n".join(lines) + "\n"
