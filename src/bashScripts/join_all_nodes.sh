if [ $# -lt 1 ]; then
    echo "Usage: $0 <node1> <node2> ... <nodeN>"
    exit 1
fi

firstNode=$(echo $1 | cut -d' ' -f1)

# Wanna join all nodes in the list on the same network
for node in $@; do
    curl -X POST -H "Content-Type: application/json" -d "" http://$node/join?nprime=$firstNode
done