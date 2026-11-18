"""Generate shell completion from the public argparse command definitions."""

import argparse
import shlex


def _commands(parser):
    return next(action.choices for action in parser._actions if isinstance(action, argparse._SubParsersAction))


def _options(parser):
    return [action for action in parser._actions if action.option_strings]


def _words(words):
    return " ".join(shlex.quote(word) for word in words)


def _value_options(commands):
    return [name + ":" + flag for name, parser in commands.items()
            for action in _options(parser) if action.nargs != 0 for flag in action.option_strings]


def _fish_position(commands):
    """Locate a positional without treating an option's value as one."""
    return [
        "function __gtm_connector_at_position",
        '  set -l command_name ""',
        "  set -l pending 0",
        "  set -l positional 0",
        "  set -l options_done 0",
        "  set -l tokens (commandline -opc)",
        "  for word in $tokens[2..-1]",
        "    if test $pending = 1",
        "      set pending 0; continue",
        "    end",
        '    if test "$word" = --',
        "      set options_done 1; continue",
        "    end",
        "    if test $options_done = 0",
        '      switch "$command_name:$word"',
        "        case " + _words(_value_options(commands)),
        "          set pending 1; continue",
        "      end",
        "      if string match -q -- '-*' $word",
        "        continue",
        "      end",
        "    end",
        '    if test -z "$command_name"',
        "      switch $word",
        "        case " + _words(commands),
        '          set command_name "$word"',
        "      end",
        "      continue",
        "    end",
        "    set positional (math $positional + 1)",
        "  end",
        '  test "$command_name" = "$argv[1]"; and test $pending = 0; and test $positional = $argv[2]',
        "end",
    ]


def generate(parser, shell):
    commands = _commands(parser)
    root = [*commands, *(flag for action in _options(parser) for flag in action.option_strings)]
    if shell == "fish":
        lines = ["# Load with: gtm-connector completion fish | source", *_fish_position(commands)]
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
            positionals = [action for action in subparser._actions if not action.option_strings
                           and not isinstance(action, argparse._SubParsersAction)]
            for index, action in enumerate(positionals):
                if action.choices:
                    at_position = f"__gtm_connector_at_position {name} {index}"
                    lines.append(f"complete -c gtm-connector -n {shlex.quote(at_position)} -f -a "
                                 + shlex.quote(" ".join(map(str, action.choices))))
        return "\n".join(lines) + "\n"
    if shell not in ("bash", "zsh"):
        raise ValueError("Choose bash, zsh, or fish.")
    bash = shell == "bash"
    function = "_gtm_connector_complete" if bash else "_gtm_connector"
    lines = [f"{function}() {{"]
    if bash:
        lines += ['  local command="" word previous="" current="" candidates="" match="" prefix=""',
                  '  local index last join_colon=0 positional=0 options_done=0',
                  '  local -a tokens', '  tokens=()', '  COMPREPLY=()',
                  # Bash splits scope values at ':' by default. Rebuild complete arguments,
                  # then return only the suffix Readline is replacing after the final colon.
                  '  for ((index=1; index<=COMP_CWORD; index++)); do',
                  '    word="${COMP_WORDS[index]}"; last=$((${#tokens[@]}-1))',
                  '    if [[ "$word" == : && $last -ge 0 ]]; then',
                  '      tokens[last]="${tokens[last]}:"; join_colon=1',
                  '    elif [[ $join_colon == 1 ]]; then',
                  '      tokens[last]="${tokens[last]}$word"; join_colon=0',
                  '    else', '      tokens+=("$word")', '    fi', '  done',
                  '  last=$((${#tokens[@]}-1)); current="${tokens[last]}"',
                  '  if [[ "$COMP_WORDBREAKS" == *:* && "$current" == *:* ]]; then',
                  '    prefix="${current%:*}:"', '  fi',
                  '  for ((index=0; index<last; index++)); do', '    word="${tokens[index]}"']
    else:
        lines += ['  local command="" word previous=""',
                  '  local current="${words[CURRENT]}" index positional=0 options_done=0',
                  '  for ((index=2; index<CURRENT; index++)); do', '    word="${words[index]}"']
    lines += ['    if [[ -n "$previous" ]]; then previous=""; continue; fi',
              '    if [[ "$word" == -- ]]; then options_done=1; continue; fi',
              '    if [[ $options_done == 0 ]]; then',
              '      case "$command:$word" in',
              '        ' + '|'.join(map(shlex.quote, _value_options(commands)))
              + ') previous="$word"; continue;;',
              '      esac', '      case "$word" in -*) continue;; esac', '    fi',
              '    if [[ -z "$command" ]]; then',
              f"      case \"$word\" in {'|'.join(commands)}) command=\"$word\";; esac",
              '      continue', '    fi', '    ((positional+=1))', '  done']

    def offer(values):
        if bash:
            return ["      candidates=" + shlex.quote(" ".join(values)),
                    '      while IFS= read -r match; do COMPREPLY+=("${match#"$prefix"}"); done '
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
    lines.append('  esac')
    lines += ['  if [[ "$current" != -* || $options_done == 1 ]]; then',
              '    case "$command:$positional" in']
    for name, subparser in commands.items():
        positionals = [action for action in subparser._actions if not action.option_strings]
        for index, action in enumerate(positionals):
            if action.choices:
                lines.append(f"    {shlex.quote(name + ':' + str(index))})")
                lines += offer(list(map(str, action.choices)))
    lines += ['    esac', '  fi']
    lines.append('  case "$command" in')
    lines.append('    "")')
    lines += offer(root)
    for name, subparser in commands.items():
        lines.append(f"    {name})")
        lines += offer([flag for action in _options(subparser) for flag in action.option_strings])
    lines += ['  esac', '}']
    lines.append(f"complete -o default -F {function} gtm-connector" if bash else "compdef _gtm_connector gtm-connector")
    return "\n".join(lines) + "\n"
