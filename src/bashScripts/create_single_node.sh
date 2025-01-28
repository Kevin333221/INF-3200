# Check arguments is provided
if [ -z "$1" ]; then
  echo "Usage: $0 <address>"
  exit 1
fi

# Check if the address is valid
if ! [[ "$1" =~ - ]]; then
  echo "Error: Argument must contain a dash."
  exit 1
fi

# Get number identifying the indentification space
if [ -z "$2" ]; then
  echo "Usage: $0 $1 <identifier space (bit)>"
  exit 1
fi

if ! [[ "$2" =~ ^[0-9]+$ ]] || [ "$2" -le 0 ]; then
  echo "Error: Argument must be a positive integer."
  exit 1
fi

port=$(shuf -i 49152-65535 -n1)

# Combines the address and port
nodePort="$1:$port"

ssh -f $1 "cd $PWD/.. && go run Server.go 0 true $nodePort $2"