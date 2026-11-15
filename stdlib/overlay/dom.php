<?php

/**
 * Phaenon overlay: the DOM lists, generic in the node they hold, and the
 * methods PHPStan's stub types with them.
 */

/**
 * @template-covariant TNode of DOMNameSpaceNode|DOMNode
 * @implements IteratorAggregate<int, TNode>
 */
class DOMNodeList implements IteratorAggregate, Countable
{
    /** @return TNode|null */
    public function item(int $index) {}
}

/**
 * PHP yields the attributes' names as the keys. PHPStan's stub passes
 * IteratorAggregate<int, TNode>, but iterates through getIterator()'s
 * Iterator<TNode>, whose key is mixed: the key passed here is that one, so
 * that no int is promised that PHP never yields.
 *
 * @template-covariant TNode of DOMNode
 * @implements IteratorAggregate<mixed, TNode>
 */
class DOMNamedNodeMap implements IteratorAggregate, Countable
{
    /** @return TNode|null */
    public function getNamedItem(string $qualifiedName): ?DOMNode {}

    /** @return TNode|null */
    public function getNamedItemNS(?string $namespace, string $localName): ?DOMNode {}

    /** @return TNode|null */
    public function item(int $index): ?DOMNode {}

    /** @return Iterator<TNode> */
    public function getIterator(): Iterator {}
}

class DOMDocument extends DOMNode implements DOMParentNode
{
    /** @return DOMNodeList<DOMElement> */
    public function getElementsByTagName(string $qualifiedName): DOMNodeList {}

    /** @return DOMNodeList<DOMElement> */
    public function getElementsByTagNameNS(?string $namespace, string $localName): DOMNodeList {}
}

class DOMElement extends DOMNode implements DOMParentNode, DOMChildNode
{
    /** @return DOMNodeList<DOMElement> */
    public function getElementsByTagName(string $qualifiedName): DOMNodeList {}

    /** @return DOMNodeList<DOMElement> */
    public function getElementsByTagNameNS(?string $namespace, string $localName): DOMNodeList {}
}
