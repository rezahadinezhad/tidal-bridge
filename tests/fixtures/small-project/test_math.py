import unittest

class TestMath(unittest.TestCase):
    def test_sum(self):
        self.assertEqual(sum(range(100)), 4950)

    def test_transform(self):
        self.assertEqual([n * n for n in range(4)], [0, 1, 4, 9])

if __name__ == "__main__":
    unittest.main()
