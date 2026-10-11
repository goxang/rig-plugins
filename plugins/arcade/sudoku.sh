#!/usr/bin/env bash
# Sudoku: arrows/hjkl move, 1-9 fill, 0/x/backspace clear, ? hint, n new puzzle, m menu, q quit.
set -u

read -r rows cols < <(stty size 2>/dev/null || echo "24 80")
margin=$(( (cols - 37) / 2 ))
(( margin < 0 )) && margin=0
pad=$(printf '%*s' "$margin" '')

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
names=(Easy Medium Hard Expert)
keep=(40 34 28 24) # clues left on the board
declare -a best=(0 0 0 0)
for i in 0 1 2 3; do
  v=0
  [[ -r $cache/sudoku.$i.best ]] && read -r v < "$cache/sudoku.$i.best"
  [[ $v =~ ^[0-9]+$ ]] && best[i]=$v
done

cleanup() { printf '\e[?25h\e[0m\e[2J\e[H'; stty sane 2>/dev/null || true; }
trap cleanup EXIT
stty -echo -icanon 2>/dev/null || true
printf '\e[?25l\e[2J'

declare -a peers=()
for ((i = 0; i < 81; i++)); do
  r=$((i / 9)) c=$((i % 9)) list=""
  for ((j = 0; j < 81; j++)); do
    (( j == i )) && continue
    jr=$((j / 9)) jc=$((j % 9))
    if (( jr == r || jc == c || (jr / 3 == r / 3 && jc / 3 == c / 3) )); then list+="$j "; fi
  done
  peers[i]=$list
done

shuffle() { # array name
  local -n a=$1
  local i j t
  for ((i = ${#a[@]} - 1; i > 0; i--)); do
    j=$(( RANDOM % (i + 1) )); t=${a[i]}; a[i]=${a[j]}; a[j]=$t
  done
}

axis() { # name: a row (or column) order that keeps the bands intact
  local -n out=$1
  local -a bands=(0 1 2) inner
  local b k
  shuffle bands
  out=()
  for b in "${bands[@]}"; do
    inner=(0 1 2); shuffle inner
    for k in "${inner[@]}"; do out+=($(( b * 3 + k ))); done
  done
}

generate() { # level
  local -a rp cp digs=(1 2 3 4 5 6 7 8 9) order=()
  local r c i q filled=81
  axis rp; axis cp; shuffle digs
  sol=()
  for ((r = 0; r < 9; r++)); do
    for ((c = 0; c < 9; c++)); do
      sol[r * 9 + c]=${digs[(3 * (rp[r] % 3) + rp[r] / 3 + cp[c]) % 9]}
    done
  done
  g=("${sol[@]}")
  for ((i = 0; i < 81; i++)); do order+=("$i"); done
  shuffle order
  for i in "${order[@]}"; do
    (( filled <= keep[$1] )) && break
    q=$(( 80 - i ))
    g[i]=0; filled=$((filled - 1))
    if (( q != i && filled > keep[$1] && g[q] != 0 )); then g[q]=0; filled=$((filled - 1)); fi
  done
  given=()
  for ((i = 0; i < 81; i++)); do (( g[i] )) && given[i]=1 || given[i]=0; done
}

new_game() {
  level=$1
  generate "$level"
  cur=40 hints=0 won=0 state=play
  started=$SECONDS
}

compute_bad() {
  bad=()
  local i j v empty=0
  for ((i = 0; i < 81; i++)); do
    v=${g[i]}
    (( v )) || { empty=$((empty + 1)); continue; }
    for j in ${peers[i]}; do
      (( g[j] == v )) && { bad[i]=1; break; }
    done
  done
  if (( empty == 0 && ${#bad[@]} == 0 && !won )); then
    won=1; finished=$(( SECONDS - started ))
    if (( best[level] == 0 || finished < best[level] )); then
      best[level]=$finished
      mkdir -p "$cache" && echo "$finished" > "$cache/sudoku.$level.best"
    fi
  fi
}

mmss() { printf '%02d:%02d' $(( $1 / 60 )) $(( $1 % 60 )); }

menu_draw() {
  local out=$'\e[H' i
  out+="$pad"$'\e[1;36m  SUDOKU\e[0m\e[K\n\e[K\n'
  out+="$pad"$'  choose a difficulty\e[K\n\e[K\n'
  for i in 0 1 2 3; do
    local mark="  " tag=""
    (( i == menu_sel )) && mark=$'\e[1;36m▸ \e[0m'
    (( best[i] )) && tag=$'\e[2m  best '"$(mmss "${best[i]}")"$'\e[0m'
    out+="$pad  $mark$((i + 1))  ${names[i]}  ${keep[i]} clues$tag"$'\e[K\n'
  done
  out+=$'\e[K\n'"$pad"$'\e[2m  1-4 or ↑/↓ + enter · q quit\e[0m\e[K\n\e[J'
  printf '%b' "$out"
}

draw() {
  compute_bad
  local out=$'\e[H' r c i v bg fg cell line
  local curv=${g[cur]}
  local -A peer=()
  for i in ${peers[cur]}; do peer[$i]=1; done
  local clock=$(( won ? finished : SECONDS - started ))
  out+="$pad"$'\e[1;36m SUDOKU\e[0m · '"${names[level]}"' · '"$(mmss "$clock")"' · hints '"$hints"
  (( best[level] )) && out+=" · best $(mmss "${best[level]}")"
  out+=$'\e[K\n'
  out+="$pad"$'\e[38;5;244m┏━━━━━━━━━━━┳━━━━━━━━━━━┳━━━━━━━━━━━┓\e[0m\e[K\n'
  for ((r = 0; r < 9; r++)); do
    line="$pad"$'\e[38;5;244m┃\e[0m'
    for ((c = 0; c < 9; c++)); do
      i=$(( r * 9 + c )); v=${g[i]}
      bg="" fg="2;37"
      if (( v )); then
        if [[ -n ${bad[i]:-} ]]; then fg="1;91"
        elif (( given[i] )); then fg="1;97"
        else fg="96"; fi
      fi
      if (( i == cur )); then bg=";48;5;31"
      elif (( curv && v == curv )); then bg=";48;5;60"
      elif [[ -n ${peer[$i]:-} ]]; then bg=";48;5;236"
      fi
      cell="·"; (( v )) && cell=$v
      line+=$'\e['"${fg}${bg}"'m '"$cell"$' \e[0m'
      if (( c % 3 == 2 )); then line+=$'\e[38;5;244m┃\e[0m'; else line+=$'\e[38;5;238m│\e[0m'; fi
    done
    out+="$line"$'\e[K\n'
    if (( r == 2 || r == 5 )); then out+="$pad"$'\e[38;5;244m┣━━━━━━━━━━━╋━━━━━━━━━━━╋━━━━━━━━━━━┫\e[0m\e[K\n'; fi
  done
  out+="$pad"$'\e[38;5;244m┗━━━━━━━━━━━┻━━━━━━━━━━━┻━━━━━━━━━━━┛\e[0m\e[K\n'
  if (( won )); then
    out+="$pad"$'\e[1;30;42m  SOLVED in '"$(mmss "$finished")"$'  · n new · m menu  \e[0m\e[K\n'
  elif (( ${#bad[@]} )); then
    out+="$pad"$'\e[91m clashing numbers are red\e[0m\e[K\n'
  else
    out+=$'\e[K\n'
  fi
  out+="$pad"$'\e[2m ←↑↓→/hjkl move · 1-9 fill · 0 clear · ? hint · n new · m menu · q quit\e[0m\e[K\n\e[J'
  printf '%b' "$out"
}

move() { # dr dc, wrapping at the edges
  local r=$(( cur / 9 + $1 )) c=$(( cur % 9 + $2 ))
  cur=$(( ((r + 9) % 9) * 9 + (c + 9) % 9 ))
}

menu_sel=1 state=menu
level=1 won=0 started=$SECONDS finished=0 cur=40
declare -a g=() sol=() given=() bad=()

while :; do
  if [[ $state == menu ]]; then menu_draw; else draw; fi
  key=""
  IFS= read -rsn1 -t 1 key || continue
  if [[ $key == $'\e' ]]; then
    seq=""
    while IFS= read -rsn1 -t 0.003 ch; do seq+=$ch; [[ $ch == [A-Za-z~] ]] && break; done
    key+=$seq
  fi
  if [[ $state == menu ]]; then
    case $key in
      [1-4]) new_game $(( key - 1 )) ;;
      $'\e[A' | k) menu_sel=$(( (menu_sel + 3) % 4 )) ;;
      $'\e[B' | j) menu_sel=$(( (menu_sel + 1) % 4 )) ;;
      '') new_game "$menu_sel" ;;
      q | Q) exit 0 ;;
    esac
    continue
  fi
  case $key in
    $'\e[A' | k | w) move -1 0 ;;
    $'\e[B' | j | s) move 1 0 ;;
    $'\e[C' | l | d) move 0 1 ;;
    $'\e[D' | h | a) move 0 -1 ;;
    [1-9]) (( won || given[cur] )) || g[cur]=$key ;;
    0 | x | X | $'\x7f' | $'\e[3~') (( won || given[cur] )) || g[cur]=0 ;;
    '?') (( won || given[cur] )) || { g[cur]=${sol[cur]}; hints=$((hints + 1)); } ;;
    n | N) new_game "$level" ;;
    m | M) state=menu; menu_sel=$level ;;
    q | Q) exit 0 ;;
  esac
done
